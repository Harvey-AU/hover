package main

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/Harvey-AU/hover/internal/jobs"
	"github.com/Harvey-AU/hover/internal/logging"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
)

var startupLog = logging.Component("startup")

/**
 * Job Queue Test Utility
 *
 * This program tests the job queue system by:
 * 1. Setting up a database connection
 * 2. Initializing the job queue schema
 * 3. Creating a worker pool with multiple workers
 * 4. Creating and starting a test job
 * 5. Monitoring job progress until completion
 *
 * Usage:
 *   go run cmd/test_jobs/main.go
 *
 * The program expects DATABASE_URL environment variable to be set in the .env file.
 */

func main() {
	// Set up logging
	logging.Setup(logging.ParseLevel("info"), "production")

	// Load environment variables
	if err := godotenv.Load(); err != nil {
		startupLog.Fatal("Error loading .env file", "error", err)
	}

	// Get database details from environment
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		startupLog.Fatal("DATABASE_URL must be set")
	}

	// Connect to database
	startupLog.Info("Connecting to PostgreSQL database...")
	database, err := db.InitFromEnv()
	if err != nil {
		startupLog.Fatal("Failed to connect to database", "error", err)
	}
	defer database.Close()

	// Set up crawler
	crawler := crawler.New(nil)

	// Create database queue for operations
	dbQueue := db.NewDbQueue(database)

	// Create worker pool
	var jobWorkers = 3
	dbConfig := &db.Config{
		DatabaseURL: dbURL,
	}
	workerPool := jobs.NewWorkerPool(database.GetDB(), dbQueue, crawler, jobWorkers, 1, dbConfig)
	workerPool.Start(context.Background())
	defer workerPool.Stop()

	startupLog.Info("Worker pool started with " + strconv.Itoa(jobWorkers) + " workers")

	// Create a test job
	jobManager := jobs.NewJobManager(database.GetDB(), dbQueue, crawler, workerPool)

	// Set up job options
	jobOptions := &jobs.JobOptions{
		Domain:                   "example.com",
		Concurrency:              2,
		FindLinks:                true,
		AllowCrossSubdomainLinks: true,
		MaxPages:                 10,
		UseSitemap:               true,
	}

	// Submit the job to the queue
	job, err := jobManager.CreateJob(context.Background(), jobOptions)
	if err != nil {
		startupLog.Fatal("Failed to create job", "error", err)
	}

	startupLog.Info("Created test job", "job_id", job.ID)

	// Add the job to the worker pool - it will automatically start processing pending tasks
	workerPool.AddJob(job.ID, jobOptions)

	startupLog.Info("Added job to worker pool, monitoring progress...", "job_id", job.ID)

	// Monitor job progress
	for {
		time.Sleep(1 * time.Second)

		job, err := jobManager.GetJobStatus(context.Background(), job.ID)
		if err != nil {
			startupLog.Error("Failed to get job status", "error", err)
			continue
		}

		startupLog.Info("Job progress",
			"status", string(job.Status),
			"progress", job.Progress,
			"completed", job.CompletedTasks,
			"failed", job.FailedTasks,
			"total", job.TotalTasks,
		)

		if job.Status == jobs.JobStatusCompleted || job.Status == jobs.JobStatusFailed {
			startupLog.Info("Job finished", "final_status", string(job.Status))
			break
		}

		if job.Status == jobs.JobStatusRunning && job.Progress >= 100.0 {
			startupLog.Info("Job complete")
			break
		}
	}
}
