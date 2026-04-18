#!/usr/bin/env node
// Run golangci-lint if available. Cross-platform (Mac + Windows).
const { spawnSync } = require("child_process");

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
