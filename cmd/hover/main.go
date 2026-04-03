// Command hover is the native CLI for the Hover application.
//
// Usage:
//
//	hover jobs generate --pr <N> --anon-key <key> [--interval 30s] [--jobs 10] [--concurrency random]
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
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
		fmt.Println("hover v0.1.0")
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
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
