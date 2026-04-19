package db

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ResetSchema performs a database reset by dropping job-related tables, clearing migration history,
// and re-running all migrations. Users and organisations are preserved.
func (db *DB) ResetSchema() error {
	startTime := time.Now()
	dbLog.Info("=== DATABASE RESET STARTED ===")
	dbLog.Info("Dropping job-related tables, clearing migrations, and rebuilding schema")
	dbLog.Info("Users and organisations will be preserved")

	// Step 0: Terminate active connections that may have locks on tables we need to drop
	dbLog.Info("Step 0/4: Terminating active backend connections to release locks")
	if err := db.terminateActiveConnections(); err != nil {
		dbLog.Warn("Failed to terminate some connections (continuing anyway)", "error", err)
	}

	// Step 1: Drop all public schema tables except users & organisations, so migrations
	// always run against a clean slate and can't conflict with leftover tables.
	dbLog.Info("Step 1/4: Dropping all public schema tables (preserving users & organisations)")

	preserved := map[string]bool{"users": true, "organisations": true}

	tableRows, err := db.client.Query(`
		SELECT tablename FROM pg_tables
		WHERE schemaname = 'public'
		ORDER BY tablename
	`)
	if err != nil {
		return fmt.Errorf("failed to list public tables: %w", err)
	}
	var tables []string
	for tableRows.Next() {
		var name string
		if err := tableRows.Scan(&name); err != nil {
			_ = tableRows.Close()
			return fmt.Errorf("failed to scan table name: %w", err)
		}
		if !preserved[name] {
			tables = append(tables, name)
		}
	}
	_ = tableRows.Close()

	tablesDropped := 0
	for i, table := range tables {
		tableStart := time.Now()
		dbLog.Info("Dropping table",
			"table", table,
			"table_num", i+1,
			"total_tables", len(tables))

		_, err := db.client.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s CASCADE`, table)) //nolint:gosec // table names sourced from pg_tables, schema scoped to public
		if err != nil {
			dbLog.Error("FAILED to drop table - reset aborted",
				"error", err,
				"table", table,
				"elapsed", time.Since(tableStart))
			return fmt.Errorf("failed to drop table %s: %w", table, err)
		}

		tablesDropped++
		dbLog.Info("Dropped table successfully",
			"table", table,
			"duration", time.Since(tableStart))
	}

	dbLog.Info("Step 1/4 completed: All public schema tables dropped",
		"tables_dropped", tablesDropped,
		"step_duration", time.Since(startTime))

	// Drop all views, functions, and triggers on preserved tables — everything
	// defined in migrations so they can be cleanly recreated.

	// Views
	viewRows, err := db.client.Query(`
		SELECT viewname FROM pg_views WHERE schemaname = 'public'
	`)
	if err != nil {
		dbLog.Warn("Failed to list public views (continuing)", "error", err)
	} else {
		var views []string
		for viewRows.Next() {
			var v string
			if err := viewRows.Scan(&v); err == nil {
				views = append(views, v)
			}
		}
		_ = viewRows.Close()
		for _, v := range views {
			if _, err := db.client.Exec(fmt.Sprintf("DROP VIEW IF EXISTS %s CASCADE", v)); err != nil { //nolint:gosec // viewname sourced from pg_views, schema scoped to public
				dbLog.Warn("Failed to drop view (continuing)", "error", err, "view", v)
			}
		}
		dbLog.Info("Public schema views dropped", "views_dropped", len(views))
	}

	// Functions
	funcRows, err := db.client.Query(`
		SELECT p.oid::regprocedure::text
		FROM pg_proc p
		JOIN pg_namespace n ON p.pronamespace = n.oid
		WHERE n.nspname = 'public'
		  AND p.prokind = 'f'
	`)
	if err != nil {
		dbLog.Warn("Failed to list public functions (continuing)", "error", err)
	} else {
		var sigs []string
		for funcRows.Next() {
			var sig string
			if err := funcRows.Scan(&sig); err == nil {
				sigs = append(sigs, sig)
			}
		}
		_ = funcRows.Close()
		for _, sig := range sigs {
			if _, err := db.client.Exec(fmt.Sprintf("DROP FUNCTION IF EXISTS %s CASCADE", sig)); err != nil { //nolint:gosec // sig sourced from pg_proc, schema scoped to public
				dbLog.Warn("Failed to drop function (continuing)", "error", err, "function", sig)
			}
		}
		dbLog.Info("Public schema functions dropped", "functions_dropped", len(sigs))
	}

	// Triggers on preserved tables (users, organisations) — others were dropped with their tables
	trigRows, err := db.client.Query(`
		SELECT trigger_name, event_object_table
		FROM information_schema.triggers
		WHERE trigger_schema = 'public'
		  AND event_object_table IN ('users', 'organisations')
	`)
	if err != nil {
		dbLog.Warn("Failed to list triggers on preserved tables (continuing)", "error", err)
	} else {
		type trigEntry struct{ name, table string }
		var trigs []trigEntry
		for trigRows.Next() {
			var t trigEntry
			if err := trigRows.Scan(&t.name, &t.table); err == nil {
				trigs = append(trigs, t)
			}
		}
		_ = trigRows.Close()
		for _, t := range trigs {
			if _, err := db.client.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s", t.name, t.table)); err != nil { //nolint:gosec // name and table sourced from information_schema, schema scoped to public
				dbLog.Warn("Failed to drop trigger (continuing)", "error", err, "trigger", t.name, "table", t.table)
			}
		}
		dbLog.Info("Triggers on preserved tables dropped", "triggers_dropped", len(trigs))
	}

	// Step 2: Clear migration history
	migrationStart := time.Now()
	dbLog.Info("Step 2/4: Clearing migration history")

	result, err := db.client.Exec(`DELETE FROM supabase_migrations.schema_migrations`)
	if err != nil {
		dbLog.Error("FAILED to clear migration history - reset incomplete",
			"error", err,
			"elapsed", time.Since(migrationStart))
		return fmt.Errorf("failed to clear migration history: %w", err)
	}

	migrationsCleared, _ := result.RowsAffected()
	dbLog.Info("Step 2/4 completed: Migration history cleared",
		"migrations_cleared", migrationsCleared,
		"step_duration", time.Since(migrationStart))

	// Step 3: Run all migrations
	executionStart := time.Now()
	dbLog.Info("Step 3/4: Running migrations from disk")

	migrationsApplied, err := db.runMigrations()
	if err != nil {
		dbLog.Error("FAILED to run migrations - database may be in inconsistent state",
			"error", err,
			"elapsed", time.Since(executionStart))
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	dbLog.Info("Step 3/4 completed: Migrations executed successfully",
		"migrations_applied", migrationsApplied,
		"step_duration", time.Since(executionStart))

	// Step 4: Verify key tables exist
	verifyStart := time.Now()
	dbLog.Info("Step 4/4: Verifying schema")

	for _, table := range []string{"domains", "pages", "jobs", "tasks"} {
		var exists bool
		err := db.client.QueryRow(`
			SELECT EXISTS (
				SELECT FROM information_schema.tables
				WHERE table_schema = 'public'
				AND table_name = $1
			)`, table).Scan(&exists)

		if err != nil || !exists {
			dbLog.Warn("Schema verification warning", "table", table, "exists", exists)
		} else {
			dbLog.Info("Table verified", "table", table)
		}
	}

	dbLog.Info("Step 4/4 completed: Schema verification complete",
		"step_duration", time.Since(verifyStart))

	totalDuration := time.Since(startTime)
	dbLog.Info("=== DATABASE RESET COMPLETED SUCCESSFULLY ===",
		"total_duration", totalDuration,
		"tables_dropped", tablesDropped,
		"migrations_cleared", migrationsCleared,
		"migrations_applied", migrationsApplied)

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
			dbLog.Info("Found migrations directory", "path", path)
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

	dbLog.Info("Found migration files", "migration_count", len(migrationFiles))

	// Execute each migration
	migrationsApplied := 0
	for i, filename := range migrationFiles {
		migrationStart := time.Now()
		dbLog.Info("Applying migration",
			"file", filename,
			"migration_num", i+1,
			"total_migrations", len(migrationFiles))

		// Read migration file
		filePath := filepath.Clean(filepath.Join(migrationsDir, filename))
		content, err := os.ReadFile(filePath) //nolint:gosec // filePath is built from internal migrations directory
		if err != nil {
			return migrationsApplied, fmt.Errorf("failed to read migration %s: %w", filename, err)
		}

		// Execute migration SQL
		_, err = db.client.Exec(string(content))
		if err != nil {
			dbLog.Error("FAILED to apply migration",
				"error", err,
				"file", filename,
				"elapsed", time.Since(migrationStart),
				"event_type", "migration_failure",
				"migration_number", i+1,
				"total_migrations", len(migrationFiles),
				"migrations_applied", migrationsApplied)
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
			dbLog.Warn("Failed to record migration in history (non-fatal)",
				"error", err,
				"migration", migrationName)
		}

		migrationsApplied++
		dbLog.Info("Migration applied successfully",
			"file", filename,
			"duration", time.Since(migrationStart))
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

	dbLog.Info("Current backend PID identified", "current_pid", currentPID)

	// Step 1: Cancel all running queries first (gentle approach)
	rows, err := db.client.Query(`
		SELECT pid, pg_cancel_backend(pid) as cancelled
		FROM pg_stat_activity
		WHERE pid <> pg_backend_pid()
		  AND datname = current_database()
		  AND state = 'active'
	`)
	if err != nil {
		dbLog.Warn("Failed to cancel queries (continuing)", "error", err)
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
		dbLog.Info("Cancelled running queries", "queries_cancelled", cancelledCount)
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

	dbLog.Info("Terminated active backend connections", "connections_terminated", terminatedCount)

	return nil
}
