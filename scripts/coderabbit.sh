#!/usr/bin/env bash
# coderabbit.sh — Cross-platform CodeRabbit CLI wrapper.
# Detects OS and routes to the appropriate coderabbit binary.
# Usage: bash scripts/coderabbit.sh [coderabbit arguments]

set -euo pipefail

CR_ARGS=("$@")

detect_and_run() {
    local os
    os="$(uname -s 2>/dev/null || echo "unknown")"

    case "$os" in
        Linux|Darwin)
            # Native Linux or macOS — use coderabbit directly
            if command -v coderabbit &>/dev/null; then
                exec coderabbit "${CR_ARGS[@]}"
            elif [ -f "$HOME/.local/bin/coderabbit" ]; then
                exec "$HOME/.local/bin/coderabbit" "${CR_ARGS[@]}"
            else
                echo "Error: coderabbit not found. Install it from https://coderabbit.ai" >&2
                exit 1
            fi
            ;;
        MINGW*|MSYS*|CYGWIN*)
            # Git Bash / MSYS2 on Windows — route through WSL
            run_via_wsl
            ;;
        *)
            # Fallback: if running inside WSL already, use directly
            if grep -qi microsoft /proc/version 2>/dev/null; then
                if command -v coderabbit &>/dev/null; then
                    exec coderabbit "${CR_ARGS[@]}"
                elif [ -f "$HOME/.local/bin/coderabbit" ]; then
                    exec "$HOME/.local/bin/coderabbit" "${CR_ARGS[@]}"
                fi
            fi
            echo "Error: unsupported environment '$os'" >&2
            exit 1
            ;;
    esac
}

run_via_wsl() {
    if ! command -v wsl &>/dev/null; then
        echo "Error: wsl not found. Ensure WSL is installed." >&2
        exit 1
    fi

    local distro="${CODERABBIT_WSL_DISTRO:-Ubuntu-24.04}"
    local cr_path="${CODERABBIT_WSL_PATH:-/home/user/.local/bin/coderabbit}"

    # Convert current Windows path to WSL mount path
    local win_path
    win_path="$(pwd -W 2>/dev/null || pwd)"
    local wsl_path
    wsl_path="$(echo "$win_path" | sed 's|\\|/|g; s|^\([A-Za-z]\):|/mnt/\L\1|')"

    # Build the argument string safely, injecting --cwd for worktree support
    local args_str=""
    local cwd_injected=false
    for arg in "${CR_ARGS[@]}"; do
        args_str="$args_str $(printf '%q' "$arg")"
        # If user already passed --cwd, don't inject again
        [[ "$arg" == "--cwd" ]] && cwd_injected=true
    done

    if [ "$cwd_injected" = false ]; then
        # Inject --cwd after the subcommand (first arg) if one exists
        if [ ${#CR_ARGS[@]} -gt 0 ]; then
            local subcmd
            subcmd="$(printf '%q' "${CR_ARGS[0]}")"
            local rest_args=""
            for arg in "${CR_ARGS[@]:1}"; do
                rest_args="$rest_args $(printf '%q' "$arg")"
            done
            args_str="$subcmd --cwd '$wsl_path'$rest_args"
        else
            args_str="--cwd '$wsl_path'"
        fi
    fi

    wsl -d "$distro" -- bash -c "$cr_path $args_str"
}

detect_and_run
