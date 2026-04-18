#!/usr/bin/env python3
"""
Aggregate JSON log summaries into time-series CSVs for agent debugging.

Outputs:
  time_series.csv         — timestamp | debug | info | warn | error
  events_per_minute.csv   — timestamp | component: message | ...
  summary.md              — human-readable overview

Usage:
    python3 scripts/aggregate_logs.py logs/20251105/0750_run/
    python3 scripts/aggregate_logs.py logs/20251105/0750_run/ --watch
    python3 scripts/aggregate_logs.py logs/20251105/0750_run/ --full
"""

import csv
import json
import sys
import time
from pathlib import Path
from collections import defaultdict
from datetime import datetime
from zoneinfo import ZoneInfo

STATE_FILE_NAME = ".aggregate_state.json"


def load_state(log_dir):
    state_file = log_dir / STATE_FILE_NAME
    if state_file.exists():
        try:
            with open(state_file) as f:
                return json.load(f)
        except Exception as e:
            print(f"Warning: Could not load state file: {e}", file=sys.stderr)
    return {"processed_files": [], "last_update": None}


def save_state(log_dir, state):
    state_file = log_dir / STATE_FILE_NAME
    melbourne_tz = ZoneInfo("Australia/Melbourne")
    state["last_update"] = datetime.now(melbourne_tz).isoformat()
    with open(state_file, "w") as f:
        json.dump(state, f, indent=2)


def _empty_minute():
    return {
        "level_counts": defaultdict(int),
        "event_counts": defaultdict(int),  # "component: message" -> count
        "component_counts": defaultdict(int),
        "samples": 0,
        "total_lines": 0,
        "failed_to_parse": 0,
    }


def load_existing_data(csv_path, events_csv_path, components_csv_path):
    """Restore aggregated state from existing CSVs for incremental runs."""
    by_minute = defaultdict(_empty_minute)

    # Level counts from time_series.csv
    if csv_path.exists():
        try:
            with open(csv_path) as f:
                next(f)  # skip header
                for line in f:
                    parts = line.strip().split(",")
                    if len(parts) >= 5:
                        ts = parts[0]
                        by_minute[ts]["level_counts"]["debug"] = int(parts[1])
                        by_minute[ts]["level_counts"]["info"] = int(parts[2])
                        by_minute[ts]["level_counts"]["warn"] = int(parts[3])
                        by_minute[ts]["level_counts"]["error"] = int(parts[4])
        except (OSError, ValueError) as e:
            print(f"Warning: Could not load time_series.csv: {e}", file=sys.stderr)

    # Event counts from events_per_minute.csv
    if events_csv_path.exists():
        try:
            with open(events_csv_path) as f:
                reader = csv.DictReader(f)
                for row in reader:
                    ts = row.pop("timestamp")
                    for event, count_str in row.items():
                        count = int(count_str)
                        if count > 0:
                            by_minute[ts]["event_counts"][event] = count
        except (OSError, csv.Error, ValueError) as e:
            print(f"Warning: Could not load events_per_minute.csv: {e}", file=sys.stderr)

    # Component counts from components_per_minute.csv
    if components_csv_path.exists():
        try:
            with open(components_csv_path) as f:
                reader = csv.DictReader(f)
                for row in reader:
                    ts = row.pop("timestamp")
                    for component, count_str in row.items():
                        count = int(count_str)
                        if count > 0:
                            by_minute[ts]["component_counts"][component] = count
        except (OSError, csv.Error, ValueError) as e:
            print(
                f"Warning: Could not load components_per_minute.csv: {e}",
                file=sys.stderr,
            )

    return by_minute


def process_json_file(json_file, by_minute):
    """Merge a single iteration JSON into the running aggregation."""
    try:
        with open(json_file) as f:
            data = json.load(f)

        meta = data.get("meta", {})
        total_lines = meta.get("total_lines", 0)
        failed_to_parse = meta.get("failed_to_parse", 0)

        # Track which minute keys this file contributes to, then add
        # file-level totals once per file (not once per minute key).
        file_minutes: set = set()
        for ts, levels in data.get("level_counts", {}).items():
            minute_key = ts[:16]
            file_minutes.add(minute_key)
            by_minute[minute_key]["samples"] += 1
            for level, count in levels.items():
                by_minute[minute_key]["level_counts"][level] += count

        # Add file-level totals to the first minute bucket to avoid double-counting.
        if file_minutes:
            first_minute = min(file_minutes)
            by_minute[first_minute]["total_lines"] += total_lines
            by_minute[first_minute]["failed_to_parse"] += failed_to_parse

        # event_counts: list of {"event": "component: message", "count": N}
        for ts, events in data.get("event_counts", {}).items():
            minute_key = ts[:16]
            for item in events:
                event = item.get("event", "unknown")
                count = item.get("count", 0)
                by_minute[minute_key]["event_counts"][event] += count

        for ts, components in data.get("component_counts", {}).items():
            minute_key = ts[:16]
            for component, count in components.items():
                by_minute[minute_key]["component_counts"][component] += count

        return True
    except Exception as e:
        print(f"Error processing {json_file}: {e}", file=sys.stderr)
        return False


def write_time_series(csv_path, by_minute):
    """timestamp | debug | info | warn | error"""
    with open(csv_path, "w") as f:
        f.write("timestamp,debug,info,warn,error\n")
        for minute in sorted(by_minute.keys()):
            levels = by_minute[minute]["level_counts"]
            f.write(
                f"{minute},"
                f"{levels.get('debug', 0)},"
                f"{levels.get('info', 0)},"
                f"{levels.get('warn', 0)},"
                f"{levels.get('error', 0)}\n"
            )


def write_events_csv(csv_path, by_minute, top_n=50):
    """timestamp | component: message | component: message | ...

    Columns are the top-N events by total count across all minutes.
    """
    # Tally global totals to pick the top-N columns.
    totals = defaultdict(int)
    for data in by_minute.values():
        for event, count in data["event_counts"].items():
            totals[event] += count

    top_events = [e for e, _ in sorted(totals.items(), key=lambda x: -x[1])[:top_n]]

    with open(csv_path, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["timestamp", *top_events])
        for minute in sorted(by_minute.keys()):
            counts = [by_minute[minute]["event_counts"].get(e, 0) for e in top_events]
            writer.writerow([minute, *counts])


def write_components_csv(csv_path, by_minute):
    """timestamp | component | component | ..."""
    all_components = sorted(
        {c for data in by_minute.values() for c in data["component_counts"]}
    )
    with open(csv_path, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["timestamp", *all_components])
        for minute in sorted(by_minute.keys()):
            counts = [by_minute[minute]["component_counts"].get(c, 0) for c in all_components]
            writer.writerow([minute, *counts])


def write_summary(summary_path, by_minute, new_files_count):
    melbourne_tz = ZoneInfo("Australia/Melbourne")
    now = datetime.now(melbourne_tz)

    total_lines = sum(m["total_lines"] for m in by_minute.values())
    total_failed = sum(m["failed_to_parse"] for m in by_minute.values())

    # Global event totals for top-20 list.
    event_totals = defaultdict(int)
    for data in by_minute.values():
        for event, count in data["event_counts"].items():
            event_totals[event] += count

    with open(summary_path, "w") as f:
        f.write("# Log Aggregation Summary\n\n")
        f.write(f"**Generated:** {now.isoformat()}\n\n")
        f.write(f"**New files processed:** {new_files_count}\n\n")
        if by_minute:
            f.write(
                f"**Time range:** {min(by_minute.keys())} to {max(by_minute.keys())}\n\n"
            )
        f.write(f"- Total log lines: **{total_lines:,}**\n")
        f.write(
            f"- Parse success rate: **{100 * (1 - total_failed / max(total_lines, 1)):.1f}%**\n\n"
        )

        # Level counts over time
        f.write("## Log Levels by Minute\n\n")
        f.write("| Timestamp | Debug | Info | Warn | Error |\n")
        f.write("|-----------|-------|------|------|-------|\n")
        for minute in sorted(by_minute.keys())[-30:]:
            levels = by_minute[minute]["level_counts"]
            f.write(
                f"| {minute} | {levels.get('debug', 0)} | {levels.get('info', 0)} |"
                f" {levels.get('warn', 0)} | {levels.get('error', 0)} |\n"
            )

        # Top events
        f.write("\n## Top 30 Events\n\n")
        f.write("| Count | Event |\n")
        f.write("|-------|-------|\n")
        for event, count in sorted(event_totals.items(), key=lambda x: -x[1])[:30]:
            f.write(f"| {count:,} | {event[:80].replace('|', '\\|')} |\n")

        warn_and_error = [
            (e, c)
            for e, c in event_totals.items()
            if any(
                kw in e.lower()
                for kw in ("error", "fail", "warn", "timeout", "panic", "killed")
            )
        ]
        if warn_and_error:
            f.write("\n## Errors & Warnings\n\n")
            f.write("| Count | Event |\n")
            f.write("|-------|-------|\n")
            for event, count in sorted(warn_and_error, key=lambda x: -x[1])[:20]:
                f.write(f"| {count:,} | {event[:80].replace('|', '\\|')} |\n")


def aggregate_logs(log_dir, incremental=True):
    log_path = Path(log_dir)
    if not log_path.exists():
        print(f"Error: Directory {log_dir} does not exist")
        return False

    csv_path = log_path / "time_series.csv"
    events_csv_path = log_path / "events_per_minute.csv"
    components_csv_path = log_path / "components_per_minute.csv"
    summary_path = log_path / "summary.md"

    state = load_state(log_path) if incremental else {"processed_files": []}
    processed_set = set(state["processed_files"])

    all_json_files = sorted(f for f in log_path.glob("*.json") if not f.name.startswith("."))
    new_files = [f for f in all_json_files if f.name not in processed_set]

    if not new_files:
        if incremental:
            print(f"No new files to process (already processed {len(processed_set)} files)")
            return True
        else:
            print(f"No JSON files found in {log_dir}")
            return False

    by_minute = (
        load_existing_data(csv_path, events_csv_path, components_csv_path)
        if incremental
        else defaultdict(_empty_minute)
    )

    print(f"Processing {len(new_files)} new files...")
    success = 0
    for json_file in new_files:
        if process_json_file(json_file, by_minute):
            processed_set.add(json_file.name)
            success += 1
            if success % 10 == 0:
                print(f"  {success}/{len(new_files)}...")

    print(f"Successfully processed {success}/{len(new_files)} new files")

    write_time_series(csv_path, by_minute)
    write_events_csv(events_csv_path, by_minute, top_n=50)
    write_components_csv(components_csv_path, by_minute)
    write_summary(summary_path, by_minute, len(new_files))

    if incremental:
        state["processed_files"] = sorted(processed_set)
        save_state(log_path, state)

    print("\nOutputs:")
    print(f"  {csv_path}")
    print(f"  {events_csv_path}")
    print(f"  {components_csv_path}")
    print(f"  {summary_path}")
    return True


def watch_mode(log_dir, interval=10):
    print(f"Watch mode: {log_dir} (every {interval}s) — Ctrl+C to stop\n")
    try:
        while True:
            aggregate_logs(log_dir, incremental=True)
            time.sleep(interval)
    except KeyboardInterrupt:
        print("\nWatch mode stopped")


if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="Aggregate JSON log summaries")
    parser.add_argument("log_dir", help="Directory containing JSON log files")
    parser.add_argument("--watch", action="store_true", help="Continuously process new files")
    parser.add_argument("--interval", type=int, default=10, help="Check interval in watch mode (default: 10s)")
    parser.add_argument("--full", action="store_true", help="Full reprocess (ignore state)")
    args = parser.parse_args()

    if args.watch:
        watch_mode(args.log_dir, args.interval)
    else:
        aggregate_logs(args.log_dir, incremental=not args.full)
