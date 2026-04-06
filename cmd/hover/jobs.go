package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

// jobsConfig holds parsed flags for "hover jobs generate".
type jobsConfig struct {
	PR              int
	AnonKey         string
	AuthURLOverride string
	APIURLOverride  string
	Interval        time.Duration
	StatusInterval  time.Duration
	JobsPerBatch    int
	Repeats         int
	Concurrency     string // "random" or integer 1-50
	Yes             bool   // skip interactive confirmation
}

func parseJobsFlags(args []string) (*jobsConfig, error) {
	c := &jobsConfig{
		Interval:       3 * time.Minute,
		StatusInterval: 30 * time.Second,
		JobsPerBatch:   3,
		Repeats:        1,
		Concurrency:    "random",
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		// Support both --flag=value and --flag value forms.
		key, val, hasEq := strings.Cut(arg, "=")
		nextVal := func() (string, error) {
			if hasEq {
				return val, nil
			}
			i++
			if i >= len(args) {
				return "", fmt.Errorf("flag %s requires a value", key)
			}
			return args[i], nil
		}

		switch key {
		case "--pr":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			n, err := parsePositiveInt("--pr", v)
			if err != nil {
				return nil, err
			}
			c.PR = n

		case "--anon-key":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			c.AnonKey = v

		case "--auth-url":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			c.AuthURLOverride = v

		case "--api-url":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			c.APIURLOverride = v

		case "--interval":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			d, err := parsePositiveDuration("--interval", v)
			if err != nil {
				return nil, err
			}
			c.Interval = d

		case "--jobs":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			n, err := parsePositiveInt("--jobs", v)
			if err != nil {
				return nil, err
			}
			c.JobsPerBatch = n

		case "--repeats":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			n, err := parsePositiveInt("--repeats", v)
			if err != nil {
				return nil, err
			}
			c.Repeats = n

		case "--concurrency":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			if v != "random" {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 || n > 50 {
					return nil, fmt.Errorf("--concurrency must be 1-50 or 'random', got: %s", v)
				}
			}
			c.Concurrency = v

		case "--status-interval":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			d, err := parsePositiveDuration("--status-interval", v)
			if err != nil {
				return nil, err
			}
			c.StatusInterval = d

		case "--yes", "-y":
			c.Yes = true

		default:
			return nil, fmt.Errorf("unknown flag: %s", key)
		}
	}

	return c, nil
}

func parsePositiveInt(flag, v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid %s value: %s", flag, v)
	}
	return n, nil
}

func parsePositiveDuration(flag, v string) (time.Duration, error) {
	d, err := parseInterval(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", flag, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive", flag)
	}
	return d, nil
}

// parseInterval handles "30s", "2m", "90" (seconds), and combined forms.
func parseInterval(s string) (time.Duration, error) {
	// If it looks like a Go duration, use that.
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Bare integer → seconds.
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("cannot parse %q as duration (use e.g. 30s, 2m)", s)
}

func (c *jobsConfig) authConfig() *authConfig {
	apiURL := "https://hover.app.goodnative.co"
	if c.PR > 0 {
		apiURL = fmt.Sprintf("https://hover-pr-%d.fly.dev", c.PR)
	}
	if c.APIURLOverride != "" {
		apiURL = c.APIURLOverride
	}

	authURL := c.AuthURLOverride
	anonKey := c.AnonKey

	// Auto-discover auth config from the target app's /config.js when not
	// explicitly overridden. This ensures preview PRs use their own Supabase
	// project rather than falling back to the production defaults.
	if authURL == "" || anonKey == "" {
		discovered := discoverConfig(apiURL)
		if authURL == "" {
			authURL = discovered.authURL
		}
		if anonKey == "" {
			anonKey = discovered.anonKey
		}
	}

	if authURL == "" {
		authURL = defaultAuthURL
	}
	if anonKey == "" {
		anonKey = defaultAnonKey
	}

	return &authConfig{
		AuthURL: authURL,
		AnonKey: anonKey,
		APIURL:  apiURL,
		PR:      c.PR,
	}
}

// isTerminal reports whether stdin is connected to an interactive terminal.
func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// scanLine reads one line from the scanner in a goroutine so the caller can
// select on ctx.Done(). Returns the scanned text or an error on EOF/cancel.
func scanLine(ctx context.Context, scanner *bufio.Scanner) (string, error) {
	type result struct {
		text string
		ok   bool
	}
	ch := make(chan result, 1)
	go func() {
		ok := scanner.Scan()
		ch <- result{scanner.Text(), ok}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		if !r.ok {
			return "", fmt.Errorf("aborted")
		}
		return r.text, nil
	}
}

// confirmOrSwitchOrg prompts the user to proceed or switch organisation.
// Skips the prompt when autoConfirm is true or stdin is not a terminal.
func confirmOrSwitchOrg(ctx context.Context, cfg *authConfig, token string, id *identity, autoConfirm bool) error {
	if autoConfirm || !isTerminal() {
		fmt.Fprintln(os.Stderr)
		return nil
	}
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprintf(os.Stderr, "\nContinue: \033[1mY\033[0m")
		if len(id.Orgs) > 1 {
			fmt.Fprintf(os.Stderr, " or Change organisation: \033[1mC\033[0m")
		}
		fmt.Fprintf(os.Stderr, " ")

		line, err := scanLine(ctx, scanner)
		if err != nil {
			return err
		}
		input := strings.TrimSpace(strings.ToLower(line))

		switch input {
		case "y", "yes":
			fmt.Fprintln(os.Stderr)
			return nil
		case "c", "change":
			if len(id.Orgs) <= 1 {
				fmt.Fprintln(os.Stderr, "No other organisations available.")
				continue
			}
			fmt.Fprintln(os.Stderr, "\nChoose organisation:")
			for i, org := range id.Orgs {
				marker := "  "
				if org.ID == id.ActiveOrgID {
					marker = "* "
				}
				fmt.Fprintf(os.Stderr, "  %s%d. %s\n", marker, i+1, org.Name)
			}
			fmt.Fprintf(os.Stderr, "\nSelect (1-%d): ", len(id.Orgs))
			line, err := scanLine(ctx, scanner)
			if err != nil {
				return err
			}
			choice, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil || choice < 1 || choice > len(id.Orgs) {
				fmt.Fprintln(os.Stderr, "Invalid selection.")
				continue
			}
			selected := id.Orgs[choice-1]
			if selected.ID == id.ActiveOrgID {
				fmt.Fprintf(os.Stderr, "Already using %s.\n", selected.Name)
				continue
			}
			if err := switchOrg(ctx, cfg, token, selected.ID); err != nil {
				return fmt.Errorf("failed to switch org: %w", err)
			}
			id.ActiveOrgID = selected.ID
			fmt.Fprintf(os.Stderr, "Switched to \033[1m%s\033[0m\n", selected.Name)
		default:
			fmt.Fprintln(os.Stderr, "Invalid input.")
		}
	}
}

// Test domains — same 115 diverse real-world sites from the shell script.
var testDomains = []string{
	// Australian businesses (6)
	"bankaust.com.au", "australiansuper.com", "bunnings.com.au",
	"jbhifi.com.au", "kmart.com.au", "officeworks.com.au",

	// E-commerce & retail (10)
	"merrypeople.com", "aesop.com", "allbirds.com", "everlane.com", "warbyparker.com",
	"casper.com", "glossier.com", "away.com", "brooklinen.com", "kotn.com",

	// Tech blogs & publications (5)
	"csswizardry.com", "heydesigner.com", "sidebar.io", "stefanjudis.com", "smolblog.com",

	// WordPress blogs & design sites (10)
	"smashingmagazine.com", "css-tricks.com", "webdesignerdepot.com", "sitepoint.com", "alistapart.com",
	"designmodo.com", "creativebloq.com", "awwwards.com", "onextrapixel.com", "hongkiat.com",

	// Small business / agency sites (9)
	"studiothink.com.au", "zeroseven.com.au", "humaan.com.au", "noice.com.au", "willandco.com.au",
	"thecontentlab.com.au", "thisisgold.com.au", "wethecollective.com.au", "tworedshoes.com.au",

	// Developer docs & tools (8)
	"fly.io", "railway.app", "render.com", "tailwindcss.com",
	"nextjs.org", "react.dev", "astro.build", "svelte.dev",

	// Additional dev frameworks & tooling (30)
	"vitejs.dev", "nuxt.com", "remix.run", "solidjs.com", "qwik.dev",
	"parceljs.org", "rollupjs.org", "esbuild.github.io", "bun.sh", "deno.com",
	"cypress.io", "vitest.dev", "pnpm.io", "turbo.build",
	"nx.dev", "oclif.io", "temporal.io", "directus.io", "strapi.io",
	"sanity.io", "payloadcms.com", "pocketbase.io", "supabase.com", "plane.so",
	"appsmith.com", "tooljet.com", "budibase.com", "windmill.dev", "tauri.app",

	// SaaS & productivity apps (12)
	"linear.app", "height.app", "reclaim.ai", "mem.ai", "reflect.app",
	"cron.com", "retool.com", "cal.com", "around.co", "raycast.com",
	"warp.dev", "cursor.so",

	// Niche e-commerce & DTC brands (17)
	"studioneat.com", "feals.com", "magicspoon.com", "atlascoffeeclub.com",
	"blueland.com", "publicgoods.com", "outerknown.com", "grovemade.com",
	"ridgewallet.com", "ouraring.com", "carawayhome.com", "maap.cc",
	"bellroy.com", "ritual.com", "cuyana.com", "thesill.com", "parachutehome.com",

	// Indie analytics & SaaS (7)
	"plausible.io", "simpleanalytics.com", "savvycal.com", "commandbar.com",
	"pirsch.io", "clarityflow.com", "swapcard.com",
}

func printJobHeader(id *identity, totalRuns, domainCount int, cfg *jobsConfig) {
	fmt.Fprintln(os.Stderr)
	if id.UserName != "" && id.ActiveOrgName() != "" {
		if len(id.Orgs) > 1 {
			fmt.Fprintf(os.Stderr, "Logged in as \033[1m%s\033[0m in \033[1m%s\033[0m [press \033[1mc\033[0m to change org]\n", id.UserName, id.ActiveOrgName())
		} else {
			fmt.Fprintf(os.Stderr, "Logged in as \033[1m%s\033[0m in \033[1m%s\033[0m\n", id.UserName, id.ActiveOrgName())
		}
	} else if id.UserName != "" {
		fmt.Fprintf(os.Stderr, "Logged in as \033[1m%s\033[0m\n", id.UserName)
	}
	fmt.Fprintf(os.Stderr, "Generating: %d total jobs across %d domains, %d per batch, %s interval", totalRuns, domainCount, cfg.JobsPerBatch, formatDuration(cfg.Interval))
	if cfg.Repeats > 1 {
		fmt.Fprintf(os.Stderr, ", %dx repeats, %s status polling", cfg.Repeats, formatDuration(cfg.StatusInterval))
	}
	fmt.Fprintln(os.Stderr)
}

func runJobsGenerate(args []string) error {
	cfg, err := parseJobsFlags(args)
	if err != nil {
		return err
	}

	ac := cfg.authConfig()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Authenticate.
	token, err := ensureToken(ctx, ac)
	if err != nil {
		return err
	}

	// Fetch identity and show confirmation prompt.
	id := fetchIdentity(ctx, ac, token)

	// Shuffle domains.
	domains := make([]string, len(testDomains))
	copy(domains, testDomains)
	rand.Shuffle(len(domains), func(i, j int) {
		domains[i], domains[j] = domains[j], domains[i]
	})

	type domainRunState struct {
		Domain               string
		RemainingRuns        int
		LastJobID            string
		LastJobStatus        string
		CompletedRuns        int
		CreateFailures       int // consecutive createJob failures for the current run slot
		StatusRefreshFails   int // consecutive fetchJobStatus failures for the active job
	}

	const (
		maxCreateRetries    = 3 // skip a run slot after this many consecutive create failures
		maxStatusRefreshErr = 3 // clear a stalled job after this many consecutive refresh failures
	)

	domainStates := make([]domainRunState, len(domains))
	for i, domain := range domains {
		domainStates[i] = domainRunState{
			Domain:        domain,
			RemainingRuns: cfg.Repeats,
		}
	}

	totalRuns := len(domains) * cfg.Repeats

	printJobHeader(id, totalRuns, len(domains), cfg)

	// Confirmation prompt — allow org switch if multiple orgs available.
	if err := confirmOrSwitchOrg(ctx, ac, token, id, cfg.Yes); err != nil {
		return err
	}

	startTime := time.Now()
	jobsCreated := 0
	batch := 0

	for jobsCreated < totalRuns {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "\nInterrupted.")
			printSummary(startTime, jobsCreated, ac.APIURL)
			return nil
		default:
		}

		// Refresh token if it's nearing expiry (within 5 minutes).
		freshToken, err := ensureToken(ctx, ac)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: token refresh failed: %v\n", err)
		} else {
			token = freshToken
		}

		for i := range domainStates {
			state := &domainStates[i]
			if state.LastJobID == "" || isTerminalJobStatus(state.LastJobStatus) {
				continue
			}

			status, err := fetchJobStatus(ctx, ac.APIURL, token, state.LastJobID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31m! Failed to refresh status for %s (%s): %v\033[0m\n", state.Domain, state.LastJobID, err)
				state.StatusRefreshFails++
				if state.StatusRefreshFails >= maxStatusRefreshErr {
					fmt.Fprintf(os.Stderr, "\033[33m! Clearing stalled job %s for %s after %d refresh failures\033[0m\n", state.LastJobID, state.Domain, state.StatusRefreshFails)
					state.LastJobID = ""
					state.LastJobStatus = ""
					state.StatusRefreshFails = 0
				}
				continue
			}
			state.StatusRefreshFails = 0
			state.LastJobStatus = status
		}

		readyIndices := make([]int, 0, cfg.JobsPerBatch)
		for i := range domainStates {
			state := &domainStates[i]
			if state.RemainingRuns == 0 {
				continue
			}
			if state.LastJobID != "" && !isTerminalJobStatus(state.LastJobStatus) {
				continue
			}
			readyIndices = append(readyIndices, i)
			if len(readyIndices) >= cfg.JobsPerBatch {
				break
			}
		}

		if len(readyIndices) == 0 {
			activeDomains := 0
			for i := range domainStates {
				if domainStates[i].RemainingRuns > 0 {
					activeDomains++
				}
			}

			fmt.Fprintf(os.Stderr, "\n\033[33mNo domains ready for the next repeat yet; polling again in %s (%d domains still queued)\033[0m\n", formatDuration(cfg.StatusInterval), activeDomains)
			select {
			case <-time.After(cfg.StatusInterval):
			case <-ctx.Done():
				fmt.Fprintln(os.Stderr, "\nInterrupted.")
				printSummary(startTime, jobsCreated, ac.APIURL)
				return nil
			}
			continue
		}

		batch++
		fmt.Fprintf(os.Stderr, "\n\033[32m=== Batch %d (%d/%d jobs created) ===\033[0m\n", batch, jobsCreated, totalRuns)

		for _, idx := range readyIndices {
			state := &domainStates[idx]
			runNumber := state.CompletedRuns + 1
			concurrency := resolveConcurrency(cfg.Concurrency)
			if cfg.Concurrency == "random" {
				fmt.Fprintf(os.Stderr, "\033[33mCreating job for %s (run %d/%d, batch %d, concurrency: %d)\033[0m\n", state.Domain, runNumber, cfg.Repeats, batch, concurrency)
			} else {
				fmt.Fprintf(os.Stderr, "\033[33mCreating job for %s (run %d/%d, batch %d)\033[0m\n", state.Domain, runNumber, cfg.Repeats, batch)
			}

			id, err := createJob(ctx, ac.APIURL, token, state.Domain, concurrency)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31m✗ Failed: %s — %v\033[0m\n", state.Domain, err)
				if strings.Contains(err.Error(), "401") {
					return fmt.Errorf("authentication failed — check your token")
				}
				state.CreateFailures++
				if state.CreateFailures >= maxCreateRetries {
					fmt.Fprintf(os.Stderr, "\033[31m✗ Skipping %s after %d failed attempts\033[0m\n", state.Domain, state.CreateFailures)
					state.RemainingRuns--
					jobsCreated++
					state.CreateFailures = 0
				}
				continue
			}
			state.CreateFailures = 0
			fmt.Fprintf(os.Stderr, "\033[32m✓ Created job %s for %s\033[0m\n", id, state.Domain)
			state.LastJobID = id
			state.LastJobStatus = "pending"
			state.RemainingRuns--
			state.CompletedRuns++
			jobsCreated++

			// Small delay between creates.
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				printSummary(startTime, jobsCreated, ac.APIURL)
				return nil
			}
		}

		if jobsCreated < totalRuns {
			fmt.Fprintf(os.Stderr, "\n\033[33mWaiting %s until next batch...\033[0m\n", formatDuration(cfg.Interval))
			select {
			case <-time.After(cfg.Interval):
			case <-ctx.Done():
				fmt.Fprintln(os.Stderr, "\nInterrupted.")
				printSummary(startTime, jobsCreated, ac.APIURL)
				return nil
			}
		}
	}

	printSummary(startTime, jobsCreated, ac.APIURL)
	return nil
}

func isTerminalJobStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func resolveConcurrency(setting string) int {
	if setting == "random" {
		return rand.IntN(41) + 10 //nolint:gosec // non-security randomisation for load spread
	}
	n, _ := strconv.Atoi(setting)
	return n
}

type jobPayload struct {
	Domain      string `json:"domain"`
	UseSitemap  bool   `json:"use_sitemap"`
	FindLinks   bool   `json:"find_links"`
	MaxPages    int    `json:"max_pages"`
	Concurrency int    `json:"concurrency"`
}

func createJob(ctx context.Context, apiURL, token, domain string, concurrency int) (string, error) {
	p := jobPayload{
		Domain:      domain,
		UseSitemap:  true,
		FindLinks:   true,
		MaxPages:    10000,
		Concurrency: concurrency,
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshalling job payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL+"/v1/jobs", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		_, _ = io.Copy(io.Discard, resp.Body)
		if reqID := resp.Header.Get("X-Request-Id"); reqID != "" {
			return "", fmt.Errorf("HTTP %d (request-id: %s)", resp.StatusCode, reqID)
		}
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	respBody, _ := io.ReadAll(resp.Body)

	// Extract job ID from response — the API may nest it differently.
	var result struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "unknown", nil
	}
	id := result.Data.ID
	if id == "" {
		id = result.Job.ID
	}
	if id == "" {
		id = result.ID
	}
	if id == "" {
		id = "unknown"
	}
	return id, nil
}

func fetchJobStatus(ctx context.Context, apiURL, token, jobID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL+"/v1/jobs/"+jobID, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		if reqID := resp.Header.Get("X-Request-Id"); reqID != "" {
			return "", fmt.Errorf("HTTP %d (request-id: %s)", resp.StatusCode, reqID)
		}
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	respBody, _ := io.ReadAll(resp.Body)

	var result struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Job struct {
			Status string `json:"status"`
		} `json:"job"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing job status response: %w", err)
	}

	status := result.Data.Status
	if status == "" {
		status = result.Job.Status
	}
	if status == "" {
		status = result.Status
	}
	if status == "" {
		return "", fmt.Errorf("job status missing in response")
	}

	return status, nil
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if s == 0 {
		if m == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", m)
	}
	return fmt.Sprintf("%dm %ds", m, s)
}

func printSummary(start time.Time, created int, apiURL string) {
	elapsed := time.Since(start)
	fmt.Fprintf(os.Stderr, "\n\033[32m=== Load test complete ===\033[0m\n\n")
	fmt.Fprintf(os.Stderr, "Jobs created:      %d\n", created)
	fmt.Fprintf(os.Stderr, "Total duration:    %s\n", formatDuration(elapsed))
	if created > 0 && elapsed.Seconds() > 0 {
		rate := float64(created) / elapsed.Seconds()
		fmt.Fprintf(os.Stderr, "Job creation rate: %.2f jobs/sec\n", rate)
	}
	fmt.Fprintf(os.Stderr, "\nCheck job status:\n  curl -H 'Authorization: Bearer $TOKEN' %s/v1/jobs\n\n", apiURL)
}
