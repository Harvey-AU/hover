# Supabase Realtime Integration

This document captures lessons learned and patterns for implementing real-time
features using Supabase Realtime.

## Overview

Supabase Realtime provides two main approaches:

1. **Postgres Changes** - Subscribe to database INSERT/UPDATE/DELETE events
2. **Broadcast** - Pub/sub messaging (requires calling `realtime.broadcast()`)

**Use Postgres Changes** for most cases. It's simpler and works automatically
when rows are inserted/updated by any source (triggers, API, direct SQL).

## Implementation Pattern

### 1. Enable Realtime on the Table

In Supabase Dashboard: Database → Tables → Select table → Enable Realtime

Or via SQL:

```sql
ALTER PUBLICATION supabase_realtime ADD TABLE notifications;
```

### 2. Add RLS Policy for Realtime

Users need SELECT access to receive change events:

```sql
CREATE POLICY "Users can receive their notifications"
ON notifications FOR SELECT
TO authenticated
USING (organisation_id IN (
  SELECT om.organisation_id FROM organisation_members om
  WHERE om.user_id = auth.uid()
));
```

### 3. Configure CSP for WebSocket

Add WebSocket URL to Content Security Policy in `middleware.go`:

```go
connect-src 'self' https://hover.auth.goodnative.co wss://hover.auth.goodnative.co ...
```

The `wss://` prefix is critical for WebSocket connections.

### 4. Client-Side Subscription

```javascript
async function subscribeToChanges() {
  const orgId = window.BB_ACTIVE_ORG?.id;
  if (!orgId || !window.supabase) {
    setTimeout(subscribeToChanges, 1000);
    return;
  }

  const channel = window.supabase
    .channel(`table-changes:${orgId}`)
    .on(
      "postgres_changes",
      {
        event: "INSERT", // or "UPDATE", "DELETE", "*"
        schema: "public",
        table: "notifications",
        filter: `organisation_id=eq.${orgId}`,
      },
      (payload) => {
        console.log("Change received:", payload);
        // Handle the change - but add a delay!
        setTimeout(() => {
          refreshData();
        }, 200);
      }
    )
    .subscribe((status) => {
      console.log("Subscription status:", status);
    });

  window.myChannel = channel;
}
```

## Critical Lessons Learned

### 1. Add Delay Before Querying

The realtime event fires when the row is inserted, but the transaction may not
be fully visible to other queries yet. Add a 200ms delay:

```javascript
.on("postgres_changes", {...}, (payload) => {
  setTimeout(() => {
    loadNotificationCount(); // Query runs after transaction commits
  }, 200);
})
```

Without this delay, your query may return stale data.

### 2. Don't Use Broadcast for Trigger Inserts

Despite documentation suggesting `realtime.broadcast_changes()` for triggers,
this function may not exist on all Supabase instances. Stick with Postgres
Changes - it works automatically for trigger-inserted rows.

### 3. RLS Must Allow SELECT

Realtime respects Row Level Security. If users can't SELECT the row, they won't
receive the change event. Ensure your RLS policy allows SELECT for the
authenticated user.

### 4. Filter by Organisation/User

Always filter subscriptions by organisation or user ID to prevent receiving
events for other tenants:

```javascript
filter: `organisation_id=eq.${orgId}`;
```

### 5. Clean Up Subscriptions

When the component unmounts or user switches org, clean up:

```javascript
if (window.myChannel) {
  window.supabase.removeChannel(window.myChannel);
}
```

## Debugging

Check browser console for:

- `Subscription status: SUBSCRIBED` - Good, connected
- `Subscription status: CHANNEL_ERROR` - Check RLS policies
- CSP violations - Check `wss://` in connect-src

Check Supabase logs for:

- Realtime connection issues
- RLS policy denials

## Example: Notifications

See `dashboard.html` around line 3100 for the working implementation:

```javascript
async function subscribeToNotifications() {
  const orgId = window.BB_ACTIVE_ORG?.id;
  if (!orgId || !window.supabase) {
    setTimeout(subscribeToNotifications, 1000);
    return;
  }

  if (window.notificationsChannel) {
    window.supabase.removeChannel(window.notificationsChannel);
  }

  const channel = window.supabase
    .channel(`notifications-changes:${orgId}`)
    .on(
      "postgres_changes",
      {
        event: "INSERT",
        schema: "public",
        table: "notifications",
        filter: `organisation_id=eq.${orgId}`,
      },
      (payload) => {
        console.log("[Realtime] Notification received:", payload);
        setTimeout(() => {
          loadNotificationCount();
          const container = document.getElementById("notificationsContainer");
          if (container?.classList.contains("open")) {
            loadNotifications();
          }
        }, 200);
      }
    )
    .subscribe((status, err) => {
      console.log("[Realtime] Subscription status:", status, err || "");
    });

  window.notificationsChannel = channel;
}
```

## Future Work

For job progress updates, apply the same pattern:

1. Enable Realtime on `jobs` table
2. Subscribe to UPDATE events with `filter: id=eq.${jobId}`
3. Update progress bar when `completed_tasks` or `status` changes
