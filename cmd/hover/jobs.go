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
	JobsPerBatch    int
	Concurrency     string // "random" or integer 1-50
	Yes             bool   // skip interactive confirmation
}

func parseJobsFlags(args []string) (*jobsConfig, error) {
	c := &jobsConfig{
		Interval:     3 * time.Minute,
		JobsPerBatch: 3,
		Concurrency:  "random",
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
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("invalid --pr value: %s", v)
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
			d, err := parseInterval(v)
			if err != nil {
				return nil, fmt.Errorf("invalid --interval: %w", err)
			}
			if d <= 0 {
				return nil, fmt.Errorf("--interval must be positive")
			}
			c.Interval = d

		case "--jobs":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("invalid --jobs value: %s", v)
			}
			c.JobsPerBatch = n

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

		case "--yes", "-y":
			c.Yes = true

		default:
			return nil, fmt.Errorf("unknown flag: %s", key)
		}
	}

	return c, nil
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
			fmt.Fprintf(os.Stderr, " or Change org: \033[1mC\033[0m")
		}
		fmt.Fprintf(os.Stderr, " ")

		if !scanner.Scan() {
			return fmt.Errorf("aborted")
		}
		input := strings.TrimSpace(strings.ToLower(scanner.Text()))

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
			if !scanner.Scan() {
				return fmt.Errorf("aborted")
			}
			choice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
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

	totalBatches := (len(domains) + cfg.JobsPerBatch - 1) / cfg.JobsPerBatch

	// Identity line.
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

	// Job settings summary.
	fmt.Fprintf(os.Stderr, "Generating: %d jobs, %d per batch, %s interval\n", len(domains), cfg.JobsPerBatch, formatDuration(cfg.Interval))

	// Confirmation prompt — allow org switch if multiple orgs available.
	if err := confirmOrSwitchOrg(ctx, ac, token, id, cfg.Yes); err != nil {
		return err
	}

	startTime := time.Now()
	jobsCreated := 0
	domainIdx := 0

	for batch := 1; batch <= totalBatches; batch++ {
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

		fmt.Fprintf(os.Stderr, "\n\033[32m=== Batch %d/%d ===\033[0m\n", batch, totalBatches)

		end := domainIdx + cfg.JobsPerBatch
		if end > len(domains) {
			end = len(domains)
		}
		batchDomains := domains[domainIdx:end]
		domainIdx = end

		for _, domain := range batchDomains {
			concurrency := resolveConcurrency(cfg.Concurrency)
			if cfg.Concurrency == "random" {
				fmt.Fprintf(os.Stderr, "\033[33mCreating job for %s (batch %d, concurrency: %d)\033[0m\n", domain, batch, concurrency)
			} else {
				fmt.Fprintf(os.Stderr, "\033[33mCreating job for %s (batch %d)\033[0m\n", domain, batch)
			}

			id, err := createJob(ctx, ac.APIURL, token, domain, concurrency)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\033[31m✗ Failed: %s — %v\033[0m\n", domain, err)
				if strings.Contains(err.Error(), "401") {
					return fmt.Errorf("authentication failed — check your token")
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "\033[32m✓ Created job %s for %s\033[0m\n", id, domain)
			jobsCreated++

			// Small delay between creates.
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				printSummary(startTime, jobsCreated, ac.APIURL)
				return nil
			}
		}

		if batch < totalBatches {
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
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, respBody)
	}

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
