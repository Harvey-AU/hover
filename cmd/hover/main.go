// Command hover is the native CLI for the Hover application.
//
// Usage:
//
//	hover jobs generate --pr <N> --anon-key <key> [--interval 30s] [--jobs 10] [--concurrency random]
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// version is set at build time via ldflags.
var version = "dev"

func main() {
	checkLatestVersion()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "jobs":
		if len(os.Args) < 3 || os.Args[2] != "generate" {
			fmt.Fprintln(os.Stderr, "Usage: hover jobs generate [flags]")
			if len(os.Args) >= 3 && (os.Args[2] == "help" || os.Args[2] == "--help" || os.Args[2] == "-h") {
				os.Exit(0)
			}
			os.Exit(1)
		}
		// Handle --help within "jobs generate" flags.
		for _, a := range os.Args[3:] {
			if a == "--help" || a == "-h" || a == "help" {
				printUsage()
				os.Exit(0)
			}
		}
		if err := runJobsGenerate(os.Args[3:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "version":
		fmt.Printf("hover v%s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

// checkLatestVersion queries the GitHub API for the latest CLI release tag
// and prints a notice if a newer version is available.
func checkLatestVersion() {
	if version == "dev" {
		return
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/Harvey-AU/hover/git/matching-refs/tags/cli-v")
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var refs []struct {
		Ref string `json:"ref"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&refs); err != nil || len(refs) == 0 {
		return
	}

	var latest string
	for _, r := range refs {
		v := strings.TrimPrefix(r.Ref, "refs/tags/cli-v")
		if compareSemver(v, latest) > 0 {
			latest = v
		}
	}
	if latest != "" && compareSemver(latest, version) > 0 {
		fmt.Fprintf(os.Stderr, "\nA newer version is available: v%s (current: v%s)\nUpdate with: npm install -g @harvey-au/hover\n", latest, version)
	}
}

// compareSemver returns >0 if a > b, <0 if a < b, 0 if equal.
func compareSemver(a, b string) int {
	parse := func(s string) [3]int {
		var parts [3]int
		_, _ = fmt.Sscanf(s, "%d.%d.%d", &parts[0], &parts[1], &parts[2]) //nolint:errcheck // best-effort parse, zero-value on failure is fine
		return parts
	}
	pa, pb := parse(a), parse(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] - pb[i]
		}
	}
	return 0
}

func printUsage() {
	usage := `hover — native CLI for the Hover application

Commands:
  jobs generate   Create load-test jobs against a preview or production instance
  version         Print version information

Usage:
  hover jobs generate --pr <N> --anon-key <key> [options]

Options:
  --pr <N>             Target preview app hover-pr-<N>.fly.dev
  --anon-key <key>     Supabase publishable key (auto-discovered if omitted)
  --interval <dur>     Batch interval (e.g. 30s, 2m) [default: 3m]
  --jobs <N>           Jobs per batch [default: 3]
  --concurrency <N>    Per-job concurrency 1-50, or "random" [default: random]
  --auth-url <url>     Override Supabase auth base URL
  --api-url <url>      Override API base URL`
	fmt.Fprintln(os.Stderr, strings.TrimSpace(usage))
}
