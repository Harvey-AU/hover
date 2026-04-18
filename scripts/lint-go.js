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
