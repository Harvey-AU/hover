package db

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/rs/zerolog/log"
)

// ResetSchema performs a database reset by dropping job-related tables, clearing migration history,
// and re-running all migrations. Users and organisations are preserved.
func (db *DB) ResetSchema() error {
	startTime := time.Now()
	log.Warn().Msg("=== DATABASE RESET STARTED ===")
	log.Warn().Msg("Dropping job-related tables, clearing migrations, and rebuilding schema")
	log.Warn().Msg("Users and organisations will be preserved")

	// Step 0: Terminate active connections that may have locks on tables we need to drop
	log.Info().Msg("Step 0/4: Terminating active backend connections to release locks")
	if err := db.terminateActiveConnections(); err != nil {
		log.Warn().Err(err).Msg("Failed to terminate some connections (continuing anyway)")
	}

	// Step 1: Drop job-related tables only (preserve users & organisations)
	log.Info().Msg("Step 1/4: Dropping job-related tables")
	tables := []string{"tasks", "jobs", "job_share_links", "pages", "domains"}
	tablesDropped := 0

	for i, table := range tables {
		tableStart := time.Now()
		log.Info().
			Str("table", table).
			Int("table_num", i+1).
			Int("total_tables", len(tables)).
			Msg("Dropping table")

		_, err := db.client.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s CASCADE`, table)) //nolint:gosec // table names are hardcoded
		if err != nil {
			log.Error().
				Err(err).
				Str("table", table).
				Dur("elapsed", time.Since(tableStart)).
				Msg("FAILED to drop table - reset aborted")
			return fmt.Errorf("failed to drop table %s: %w", table, err)
		}

		tablesDropped++
		log.Info().
			Str("table", table).
			Dur("duration", time.Since(tableStart)).
			Msg("Dropped table successfully")
	}

	log.Info().
		Int("tables_dropped", tablesDropped).
		Dur("step_duration", time.Since(startTime)).
		Msg("Step 1/4 completed: Job-related tables dropped")

	// Clean up database functions that may conflict with migrations when re-applied.
	log.Info().Msg("Cleaning up queue helper functions")
	functionSignatures := []string{
		"promote_waiting_task_for_job(UUID)",
		"promote_waiting_task_for_job(TEXT)",
		"job_has_capacity(UUID)",
		"job_has_capacity(TEXT)",
	}
	for _, signature := range functionSignatures {
		dropStart := time.Now()
		if _, err := db.client.Exec(fmt.Sprintf("DROP FUNCTION IF EXISTS %s", signature)); err != nil { //nolint:gosec // function signatures are hardcoded
			log.Warn().Err(err).Str("function", signature).Msg("Failed to drop function during reset")
		} else {
			log.Info().Str("function", signature).Dur("duration", time.Since(dropStart)).Msg("Dropped function if existed")
		}
	}

	// Step 2: Clear migration history
	migrationStart := time.Now()
	log.Info().Msg("Step 2/4: Clearing migration history")

	result, err := db.client.Exec(`DELETE FROM supabase_migrations.schema_migrations`)
	if err != nil {
		log.Error().
			Err(err).
			Dur("elapsed", time.Since(migrationStart)).
			Msg("FAILED to clear migration history - reset incomplete")
		return fmt.Errorf("failed to clear migration history: %w", err)
	}

	migrationsCleared, _ := result.RowsAffected()
	log.Info().
		Int64("migrations_cleared", migrationsCleared).
		Dur("step_duration", time.Since(migrationStart)).
		Msg("Step 2/4 completed: Migration history cleared")

	// Step 3: Run all migrations
	executionStart := time.Now()
	log.Info().Msg("Step 3/4: Running migrations from disk")

	migrationsApplied, err := db.runMigrations()
	if err != nil {
		log.Error().
			Err(err).
			Dur("elapsed", time.Since(executionStart)).
			Msg("FAILED to run migrations - database may be in inconsistent state")
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	log.Info().
		Int("migrations_applied", migrationsApplied).
		Dur("step_duration", time.Since(executionStart)).
		Msg("Step 3/4 completed: Migrations executed successfully")

	// Step 4: Verify key tables exist
	verifyStart := time.Now()
	log.Info().Msg("Step 4/4: Verifying schema")

	for _, table := range []string{"domains", "pages", "jobs", "tasks"} {
		var exists bool
		err := db.client.QueryRow(`
			SELECT EXISTS (
				SELECT FROM information_schema.tables
				WHERE table_schema = 'public'
				AND table_name = $1
			)`, table).Scan(&exists)

		if err != nil || !exists {
			log.Warn().
				Str("table", table).
				Bool("exists", exists).
				Msg("Schema verification warning")
		} else {
			log.Info().
				Str("table", table).
				Msg("Table verified")
		}
	}

	log.Info().
		Dur("step_duration", time.Since(verifyStart)).
		Msg("Step 4/4 completed: Schema verification complete")

	totalDuration := time.Since(startTime)
	log.Warn().
		Dur("total_duration", totalDuration).
		Int("tables_dropped", tablesDropped).
		Int64("migrations_cleared", migrationsCleared).
		Int("migrations_applied", migrationsApplied).
		Msg("=== DATABASE RESET COMPLETED SUCCESSFULLY ===")

	return nil
}

// runMigrations reads all migration files from supabase/migrations/ and executes them in order
func (db *DB) runMigrations() (int, error) {
	// Find migrations directory - try multiple possible locations
	migrationsDir := ""
	possiblePaths := []string{
		"supabase/migrations",
		"./supabase/migrations",
		"../supabase/migrations",
		"../../supabase/migrations",
	}

	for _, path := range possiblePaths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			migrationsDir = path
			log.Info().Str("path", path).Msg("Found migrations directory")
			break
		}
	}

	if migrationsDir == "" {
		return 0, fmt.Errorf("migrations directory not found - tried: %v", possiblePaths)
	}

	// Read all .sql files
	files, err := os.ReadDir(migrationsDir)
	if err != nil {
		return 0, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	// Filter and sort migration files
	var migrationFiles []string
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".sql") {
			migrationFiles = append(migrationFiles, file.Name())
		}
	}
	sort.Strings(migrationFiles) // Ensures chronological order by timestamp prefix

	if len(migrationFiles) == 0 {
		return 0, fmt.Errorf("no migration files found in %s", migrationsDir)
	}

	log.Info().
		Int("migration_count", len(migrationFiles)).
		Msg("Found migration files")

	// Execute each migration
	migrationsApplied := 0
	for i, filename := range migrationFiles {
		migrationStart := time.Now()
		log.Info().
			Str("file", filename).
			Int("migration_num", i+1).
			Int("total_migrations", len(migrationFiles)).
			Msg("Applying migration")

		// Read migration file
		filePath := filepath.Clean(filepath.Join(migrationsDir, filename))
		content, err := os.ReadFile(filePath) //nolint:gosec // filePath is built from internal migrations directory
		if err != nil {
			return migrationsApplied, fmt.Errorf("failed to read migration %s: %w", filename, err)
		}

		// Execute migration SQL
		_, err = db.client.Exec(string(content))
		if err != nil {
			log.Error().
				Err(err).
				Str("file", filename).
				Dur("elapsed", time.Since(migrationStart)).
				Msg("FAILED to apply migration")

			// Capture catastrophic migration failure in Sentry
			sentry.WithScope(func(scope *sentry.Scope) {
				scope.SetLevel(sentry.LevelError)
				scope.SetTag("event_type", "migration_failure")
				scope.SetTag("migration_file", filename)
				scope.SetContext("migration_details", map[string]any{
					"file":               filename,
					"migration_number":   i + 1,
					"total_migrations":   len(migrationFiles),
					"migrations_applied": migrationsApplied,
					"duration_ms":        time.Since(migrationStart).Milliseconds(),
					"error":              err.Error(),
				})
				sentry.CaptureException(err)
			})

			return migrationsApplied, fmt.Errorf("failed to execute migration %s: %w", filename, err)
		}

		// Record migration in history using Supabase's expected version/name format
		migrationName := strings.TrimSuffix(filename, ".sql")
		version := migrationName
		name := migrationName
		if parts := strings.SplitN(migrationName, "_", 2); len(parts) == 2 {
			version = parts[0]
			name = parts[1]
		}
		_, err = db.client.Exec(`
			INSERT INTO supabase_migrations.schema_migrations (version, name, statements)
			VALUES ($1, $2, ARRAY[''])
			ON CONFLICT (version) DO NOTHING
		`, version, name)
		if err != nil {
			log.Warn().
				Err(err).
				Str("migration", migrationName).
				Msg("Failed to record migration in history (non-fatal)")
		}

		migrationsApplied++
		log.Info().
			Str("file", filename).
			Dur("duration", time.Since(migrationStart)).
			Msg("Migration applied successfully")
	}

	return migrationsApplied, nil
}

// terminateActiveConnections terminates all backend connections except the current one
// This releases locks on tables/rows so we can perform DROP TABLE operations
func (db *DB) terminateActiveConnections() error {
	// Get current backend PID to avoid terminating our own connection
	var currentPID int
	err := db.client.QueryRow("SELECT pg_backend_pid()").Scan(&currentPID)
	if err != nil {
		return fmt.Errorf("failed to get current backend PID: %w", err)
	}

	log.Info().Int("current_pid", currentPID).Msg("Current backend PID identified")

	// Step 1: Cancel all running queries first (gentle approach)
	rows, err := db.client.Query(`
		SELECT pid, pg_cancel_backend(pid) as cancelled
		FROM pg_stat_activity
		WHERE pid <> pg_backend_pid()
		  AND datname = current_database()
		  AND state = 'active'
	`)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to cancel queries (continuing)")
	} else {
		cancelledCount := 0
		for rows.Next() {
			var pid int
			var cancelled bool
			if err := rows.Scan(&pid, &cancelled); err == nil && cancelled {
				cancelledCount++
			}
		}
		_ = rows.Close()
		log.Info().Int("queries_cancelled", cancelledCount).Msg("Cancelled running queries")
	}

	// Step 2: Wait briefly for cancellations to take effect
	time.Sleep(100 * time.Millisecond)

	// Step 3: Terminate all other backend connections
	rows, err = db.client.Query(`
		SELECT pid, pg_terminate_backend(pid) as terminated
		FROM pg_stat_activity
		WHERE pid <> pg_backend_pid()
		  AND datname = current_database()
	`)
	if err != nil {
		return fmt.Errorf("failed to terminate connections: %w", err)
	}
	defer rows.Close()

	terminatedCount := 0
	for rows.Next() {
		var pid int
		var terminated bool
		if err := rows.Scan(&pid, &terminated); err == nil && terminated {
			terminatedCount++
		}
	}

	log.Info().
		Int("connections_terminated", terminatedCount).
		Msg("Terminated active backend connections")

	return nil
}
