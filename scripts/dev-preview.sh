#!/bin/bash
# Starts Supabase (no-op if already running) then Air with hot reloading.
# Used by .claude/launch.json for the Claude Code preview feature.
set -e

supabase start
exec air
