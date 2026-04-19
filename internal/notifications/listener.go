package notifications

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"time"

	"github.com/Harvey-AU/hover/internal/logging"
	"github.com/lib/pq"
)

var notifyLog = logging.Component("notify")

// Listener listens for PostgreSQL notifications and triggers delivery
type Listener struct {
	connStr string
	service *Service
}

// NewListener creates a new notification listener.
// Returns nil if service is nil to prevent nil pointer dereferences.
func NewListener(connStr string, service *Service) *Listener {
	if service == nil {
		notifyLog.Error("Cannot create notification listener: service is nil")
		return nil
	}
	return &Listener{
		connStr: connStr,
		service: service,
	}
}

// Start begins listening for notifications
// It uses PostgreSQL LISTEN/NOTIFY for real-time delivery
// Falls back to polling if the connection fails
func (l *Listener) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			notifyLog.Info("Notification listener stopped")
			return
		default:
			if err := l.listen(ctx); err != nil {
				notifyLog.Warn("Notification listener error, retrying in 5s", "error", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}
		}
	}
}

func (l *Listener) listen(ctx context.Context) error {
	// Create a dedicated connection for LISTEN
	listener := pq.NewListener(l.connStr, 10*time.Second, time.Minute, func(ev pq.ListenerEventType, err error) {
		if err != nil {
			notifyLog.Warn("Notification listener event error", "error", err)
		}
	})
	defer listener.Close()

	if err := listener.Listen("new_notification"); err != nil {
		return err
	}

	notifyLog.Info("Notification listener started (real-time mode)")

	// Process any pending notifications on startup
	l.processPending(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil

		case notification := <-listener.Notify:
			if notification == nil {
				// Connection lost, reconnect
				return nil
			}

			notifyLog.Debug("Received notification",
				"channel", notification.Channel,
				"payload", notification.Extra,
			)

			// Process pending notifications (the payload is the notification ID,
			// but we process all pending to handle any that might have been missed)
			l.processPending(ctx)

		case <-time.After(90 * time.Second):
			// Ping to keep connection alive
			if err := listener.Ping(); err != nil {
				return err
			}
		}
	}
}

func (l *Listener) processPending(ctx context.Context) {
	if err := l.service.ProcessPendingNotifications(ctx, 50); err != nil {
		if ctx.Err() == nil {
			notifyLog.Warn("Failed to process pending notifications", "error", err)
		}
	}
}

// StartWithFallback starts the listener with polling fallback.
// This is useful when the database doesn't support LISTEN (e.g., connection poolers).
// If DATABASE_DIRECT_URL is set, tests the connection first and uses it for real-time LISTEN/NOTIFY.
// Falls back to polling if the direct connection fails or is unavailable.
func StartWithFallback(ctx context.Context, connStr string, service *Service) {
	// Check for direct connection URL (bypasses pooler for LISTEN/NOTIFY)
	directURL := os.Getenv("DATABASE_DIRECT_URL")
	if directURL != "" {
		// Test the direct connection before committing to listener mode
		if testConnection(directURL) {
			listener := NewListener(directURL, service)
			if listener != nil {
				notifyLog.Info("Notification listener started (real-time via DATABASE_DIRECT_URL)")
				go listener.Start(ctx)
				return
			}
		} else {
			notifyLog.Warn("DATABASE_DIRECT_URL connection failed, falling back to polling")
		}
	}

	// Try to use LISTEN/NOTIFY with main connection
	listener := NewListener(connStr, service)
	if listener == nil {
		// NewListener only returns nil when service is nil; starting the
		// polling loop against a nil service would panic on the first tick.
		notifyLog.Warn("Notification listener not created; notifications disabled")
		return
	}

	// Check if we can use LISTEN (direct connection, not pooled)
	if canUseListen(connStr) {
		go listener.Start(ctx)
		return
	}

	// Fall back to polling
	notifyLog.Info("Using polling mode for notifications (connection pooler detected)")
	go startPolling(ctx, service)
}

// testConnection tests if a database connection can be established.
func testConnection(connStr string) bool {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		notifyLog.Debug("Failed to open direct connection", "error", err)
		return false
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		notifyLog.Debug("Failed to ping direct connection", "error", err)
		return false
	}
	return true
}

// canUseListen checks if the connection string supports LISTEN/NOTIFY.
// Connection poolers like PgBouncer in transaction mode don't support LISTEN.
func canUseListen(connStr string) bool {
	// Supabase's pooler URLs contain "pooler" in the host
	if strings.Contains(connStr, "pooler") {
		return false
	}
	// PgBouncer typically runs on port 6543
	if strings.Contains(connStr, ":6543") {
		return false
	}
	return true
}

func startPolling(ctx context.Context, service *Service) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	notifyLog.Info("Notification processor started (polling mode)")

	for {
		select {
		case <-ctx.Done():
			notifyLog.Info("Notification processor stopped")
			return
		case <-ticker.C:
			if err := service.ProcessPendingNotifications(ctx, 50); err != nil {
				if ctx.Err() == nil {
					notifyLog.Warn("Failed to process pending notifications", "error", err)
				}
			}
		}
	}
}
