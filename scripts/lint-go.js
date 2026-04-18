#!/usr/bin/env node
// Run golangci-lint only when Go files are staged. Cross-platform (Mac + Windows).
const { spawnSync } = require("child_process");

// Check for staged Go files first — skip entirely if none.
const staged = spawnSync(
  "git",
  ["diff", "--cached", "--name-only", "--", "*.go"],
  {
    encoding: "utf8",
    shell: false,
  }
);
// Fail closed: if git itself errored, don't silently skip linting.
if (staged.error || staged.status !== 0) {
  console.error(staged.stderr || "Error: failed to inspect staged Go files.");
  process.exit(staged.status ?? 1);
}
if (!staged.stdout || !staged.stdout.trim()) {
  process.exit(0);
}

const check = spawnSync("golangci-lint", ["--version"], {
  stdio: "ignore",
  shell: false,
});
if (check.error) {
  console.log(
    "Warning: golangci-lint not installed, skipping local lint (will still run in CI)"
  );
  process.exit(0);
}

const result = spawnSync(
  "golangci-lint",
  ["run", "--config", ".golangci.yml", "--fast-only"],
  { stdio: "inherit", shell: false }
);
process.exit(result.status ?? 1);
