package jobs

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/Harvey-AU/hover/internal/crawler"
	"github.com/Harvey-AU/hover/internal/db"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/publicsuffix"
)

// LinkDiscoveryDeps bundles the dependencies that ProcessDiscoveredLinks
// requires, allowing both WorkerPool and StreamWorkerPool to supply them.
type LinkDiscoveryDeps struct {
	DBQueue     DbQueueInterface
	JobManager  JobManagerInterface
	Logger      zerolog.Logger
	MinPriority float64 // linkDiscoveryMinPriority threshold
}

// ProcessDiscoveredLinks handles link processing and enqueueing for discovered
// URLs. It filters links for same-site compliance and robots.txt rules,
// creates page records, and enqueues new tasks via the JobManager.
//
// Priority updates are NOT performed here — the caller is responsible for
// running updateTaskPriorities (or equivalent) after this function returns.
func ProcessDiscoveredLinks(ctx context.Context, deps LinkDiscoveryDeps, task *Task, links map[string][]string, sourceURL string, robotsRules *crawler.RobotsRules) {
	log.Debug().
		Str("task_id", task.ID).
		Int("total_links_found", len(links["header"])+len(links["footer"])+len(links["body"])).
		Bool("find_links_enabled", task.FindLinks).
		Msg("Starting link processing and priority assignment")

	// Use domain ID from task (already populated from job cache).
	domainID := task.DomainID
	if domainID == 0 {
		log.Error().
			Str("task_id", task.ID).
			Str("job_id", task.JobID).
			Msg("Missing domain ID; skipping link processing")
		return
	}

	isHomepage := task.Path == "/"

	processLinkCategory := func(links []string, priority float64) {
		if len(links) == 0 {
			return
		}
		if priority < deps.MinPriority {
			log.Debug().
				Str("task_id", task.ID).
				Float64("priority", priority).
				Float64("min_priority", deps.MinPriority).
				Msg("Skipping discovered link persistence below priority threshold")
			return
		}

		baseURL, baseErr := url.Parse(sourceURL)

		if err := ctx.Err(); err != nil {
			log.Debug().
				Err(err).
				Str("job_id", task.JobID).
				Str("domain", task.DomainName).
				Str("task_id", task.ID).
				Msg("Skipping discovered link processing: parent task context is done")
			return
		}

		// 1. Filter links for same-site and robots.txt compliance.
		var filtered []string
		var blockedCount int
		for _, link := range links {
			linkURL, err := url.Parse(link)
			if err != nil {
				continue
			}

			if !linkURL.IsAbs() {
				if baseErr != nil || baseURL == nil {
					continue
				}
				linkURL = baseURL.ResolveReference(linkURL)
			}

			if isLinkAllowedForTask(linkURL.Hostname(), task) {
				linkURL.Fragment = ""
				if linkURL.Path != "/" && strings.HasSuffix(linkURL.Path, "/") {
					linkURL.Path = strings.TrimSuffix(linkURL.Path, "/")
				}

				// Check robots.txt rules.
				if robotsRules != nil && !crawler.IsPathAllowed(robotsRules, linkURL.Path) {
					blockedCount++
					log.Debug().
						Str("url", linkURL.String()).
						Str("path", linkURL.Path).
						Str("source", sourceURL).
						Msg("Link blocked by robots.txt")
					continue
				}

				filtered = append(filtered, linkURL.String())
			}
		}

		if blockedCount > 0 {
			log.Debug().
				Str("task_id", task.ID).
				Int("blocked_count", blockedCount).
				Int("allowed_count", len(filtered)).
				Msg("Filtered discovered links against robots.txt")
		}

		if len(filtered) == 0 {
			return
		}

		linkCtxTimeout := discoveredLinksDBTimeout
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= discoveredLinksMinRemain {
				log.Warn().
					Str("job_id", task.JobID).
					Str("domain", task.DomainName).
					Str("task_id", task.ID).
					Dur("remaining", remaining).
					Msg("Skipping discovered link persistence: task deadline too close")
				return
			}

			maxTimeout := remaining - discoveredLinksMinRemain
			if maxTimeout < linkCtxTimeout {
				linkCtxTimeout = maxTimeout
			}
		}
		if linkCtxTimeout < discoveredLinksMinTimeout {
			log.Warn().
				Str("job_id", task.JobID).
				Str("domain", task.DomainName).
				Str("task_id", task.ID).
				Dur("timeout", linkCtxTimeout).
				Msg("Skipping discovered link persistence: insufficient timeout budget")
			return
		}
		// Keep request-scoped values while detaching from parent cancellation/deadline.
		linkCtx, linkCancel := context.WithTimeout(context.WithoutCancel(ctx), linkCtxTimeout)
		defer linkCancel()

		// 2. Create page records.
		pageIDs, hosts, paths, err := db.CreatePageRecords(linkCtx, deps.DBQueue, domainID, task.DomainName, filtered)
		if err != nil {
			log.Error().Err(err).Msg("Failed to create page records for links")
			return
		}

		// 3. Build a slice of db.Page for enqueueing.
		pagesToEnqueue := make([]db.Page, len(pageIDs))
		for i := range pageIDs {
			pagesToEnqueue[i] = db.Page{
				ID:       pageIDs[i],
				Host:     hosts[i],
				Path:     paths[i],
				Priority: priority,
			}
		}

		// 4. Enqueue new tasks.
		if err := deps.JobManager.EnqueueJobURLs(linkCtx, task.JobID, pagesToEnqueue, "link", sourceURL); err != nil {
			log.Error().Err(err).Msg("Failed to enqueue discovered links")
			return
		}
	}

	// Apply priorities based on page type and link category.
	if isHomepage {
		log.Debug().Str("task_id", task.ID).Msg("Processing links from HOMEPAGE")
		processLinkCategory(links["header"], 1.000)
		processLinkCategory(links["footer"], 0.990)
		processLinkCategory(links["body"], task.PriorityScore*0.9)
	} else {
		log.Debug().Str("task_id", task.ID).Msg("Processing links from regular page")
		processLinkCategory(links["body"], task.PriorityScore*0.9)
	}
}

// ---------------------------------------------------------------------------
// Pure helper functions (no WorkerPool dependency)
// ---------------------------------------------------------------------------

// normaliseComparableHost strips leading "www.", trailing dots, and lowercases.
func normaliseComparableHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	return strings.TrimPrefix(host, "www.")
}

// sameRegistrableDomain returns true when both hosts share the same
// registrable domain (eTLD+1).
func sameRegistrableDomain(hostA, hostB string) bool {
	normalisedA := normaliseComparableHost(hostA)
	normalisedB := normaliseComparableHost(hostB)
	if normalisedA == "" || normalisedB == "" {
		return false
	}

	rootA, errA := publicsuffix.EffectiveTLDPlusOne(normalisedA)
	rootB, errB := publicsuffix.EffectiveTLDPlusOne(normalisedB)
	if errA != nil || errB != nil {
		return normalisedA == normalisedB
	}

	return rootA == rootB
}

// sameHostWithWWWEquivalence treats "www.example.com" and "example.com" as
// equivalent after normalisation.
func sameHostWithWWWEquivalence(hostA, hostB string) bool {
	return normaliseComparableHost(hostA) == normaliseComparableHost(hostB)
}

// isLinkAllowedForTask determines whether a discovered hostname should be
// queued for the given task, respecting cross-subdomain and www-equivalence
// policies.
func isLinkAllowedForTask(discoveredHost string, task *Task) bool {
	if task == nil {
		return false
	}

	if task.AllowCrossSubdomainLinks {
		return sameRegistrableDomain(discoveredHost, task.DomainName)
	}

	if task.Host != "" {
		return sameHostWithWWWEquivalence(discoveredHost, task.Host)
	}

	return sameHostWithWWWEquivalence(discoveredHost, task.DomainName)
}
