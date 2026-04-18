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

# Lossless state persisted between incremental runs.
DATA_FILE_NAME = ".aggregate_data.json"


def _empty_minute():
    return {
        "level_counts": defaultdict(int),
        "event_counts": defaultdict(int),  # "component: message" -> count
        "component_counts": defaultdict(int),
        "samples": 0,
    }


def _empty_totals():
    return {"total_lines": 0, "failed_to_parse": 0}


def load_data(log_dir):
    """Load full aggregate state from the lossless JSON data file."""
    data_file = log_dir / DATA_FILE_NAME
    if data_file.exists():
        try:
            with open(data_file) as f:
                saved = json.load(f)

            by_minute = defaultdict(_empty_minute)
            for minute, bucket in saved.get("by_minute", {}).items():
                by_minute[minute]["samples"] = bucket.get("samples", 0)
                by_minute[minute]["level_counts"] = defaultdict(
                    int, bucket.get("level_counts", {})
                )
                by_minute[minute]["event_counts"] = defaultdict(
                    int, bucket.get("event_counts", {})
                )
                by_minute[minute]["component_counts"] = defaultdict(
                    int, bucket.get("component_counts", {})
                )

            totals = {
                "total_lines": saved.get("total_lines", 0),
                "failed_to_parse": saved.get("failed_to_parse", 0),
            }
            processed_files = saved.get("processed_files", [])
            return by_minute, totals, processed_files
        except (OSError, json.JSONDecodeError, KeyError, TypeError) as e:
            print(f"Warning: Could not load data file: {e}", file=sys.stderr)

    return defaultdict(_empty_minute), _empty_totals(), []


def save_data(log_dir, by_minute, totals, processed_files):
    """Persist full aggregate state to the lossless JSON data file."""
    melbourne_tz = ZoneInfo("Australia/Melbourne")
    data = {
        "last_update": datetime.now(melbourne_tz).isoformat(),
        "processed_files": sorted(processed_files),
        "total_lines": totals["total_lines"],
        "failed_to_parse": totals["failed_to_parse"],
        "by_minute": {
            minute: {
                "samples": bucket["samples"],
                "level_counts": dict(bucket["level_counts"]),
                "event_counts": dict(bucket["event_counts"]),
                "component_counts": dict(bucket["component_counts"]),
            }
            for minute, bucket in by_minute.items()
        },
    }
    data_file = log_dir / DATA_FILE_NAME
    with open(data_file, "w") as f:
        json.dump(data, f, separators=(",", ":"))


def process_json_file(json_file, by_minute, totals):
    """Merge a single iteration JSON into the running aggregation."""
    try:
        with open(json_file) as f:
            data = json.load(f)

        meta = data.get("meta", {})
        file_total_lines = meta.get("total_lines", 0)
        file_failed = meta.get("failed_to_parse", 0)

        # Stage all changes locally so a mid-parse error leaves no partial state.
        staged: dict = defaultdict(_empty_minute)

        for ts, levels in data.get("level_counts", {}).items():
            minute_key = ts[:16]
            staged[minute_key]["samples"] += 1
            for level, count in levels.items():
                staged[minute_key]["level_counts"][level] += count

        # event_counts: list of {"event": "component: message", "count": N}
        for ts, events in data.get("event_counts", {}).items():
            minute_key = ts[:16]
            for item in events:
                event = item.get("event", "unknown")
                count = item.get("count", 0)
                staged[minute_key]["event_counts"][event] += count

        for ts, components in data.get("component_counts", {}).items():
            minute_key = ts[:16]
            for component, count in components.items():
                staged[minute_key]["component_counts"][component] += count

        # Parsing succeeded — commit atomically.
        totals["total_lines"] += file_total_lines
        totals["failed_to_parse"] += file_failed
        for minute_key, bucket in staged.items():
            by_minute[minute_key]["samples"] += bucket["samples"]
            for level, count in bucket["level_counts"].items():
                by_minute[minute_key]["level_counts"][level] += count
            for event, count in bucket["event_counts"].items():
                by_minute[minute_key]["event_counts"][event] += count
            for component, count in bucket["component_counts"].items():
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
    totals = defaultdict(int)
    for bucket in by_minute.values():
        for event, count in bucket["event_counts"].items():
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
        {c for bucket in by_minute.values() for c in bucket["component_counts"]}
    )
    with open(csv_path, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["timestamp", *all_components])
        for minute in sorted(by_minute.keys()):
            counts = [by_minute[minute]["component_counts"].get(c, 0) for c in all_components]
            writer.writerow([minute, *counts])


def write_summary(summary_path, by_minute, totals, new_files_count):
    melbourne_tz = ZoneInfo("Australia/Melbourne")
    now = datetime.now(melbourne_tz)

    total_lines = totals["total_lines"]
    total_failed = totals["failed_to_parse"]

    event_totals = defaultdict(int)
    for bucket in by_minute.values():
        for event, count in bucket["event_counts"].items():
            event_totals[event] += count

    # Time range excludes any non-timestamp sentinel keys (defensive).
    ts_keys = sorted(by_minute.keys())

    with open(summary_path, "w") as f:
        f.write("# Log Aggregation Summary\n\n")
        f.write(f"**Generated:** {now.isoformat()}\n\n")
        f.write(f"**New files processed:** {new_files_count}\n\n")
        if ts_keys:
            f.write(f"**Time range:** {ts_keys[0]} to {ts_keys[-1]}\n\n")
        f.write(f"- Total log lines: **{total_lines:,}**\n")
        f.write(
            f"- Parse success rate: **{100 * (1 - total_failed / max(total_lines, 1)):.1f}%**\n\n"
        )

        f.write("## Log Levels by Minute\n\n")
        f.write("| Timestamp | Debug | Info | Warn | Error |\n")
        f.write("|-----------|-------|------|------|-------|\n")
        for minute in ts_keys[-30:]:
            levels = by_minute[minute]["level_counts"]
            f.write(
                f"| {minute} | {levels.get('debug', 0)} | {levels.get('info', 0)} |"
                f" {levels.get('warn', 0)} | {levels.get('error', 0)} |\n"
            )

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

    if incremental:
        by_minute, totals, processed_list = load_data(log_path)
        processed_set = set(processed_list)
    else:
        by_minute, totals, processed_set = defaultdict(_empty_minute), _empty_totals(), set()

    all_json_files = sorted(f for f in log_path.glob("*.json") if not f.name.startswith("."))
    new_files = [f for f in all_json_files if f.name not in processed_set]

    if not new_files:
        if incremental:
            print(f"No new files to process (already processed {len(processed_set)} files)")
            return True
        else:
            print(f"No JSON files found in {log_dir}")
            return False

    print(f"Processing {len(new_files)} new files...")
    success = 0
    for json_file in new_files:
        if process_json_file(json_file, by_minute, totals):
            processed_set.add(json_file.name)
            success += 1
            if success % 10 == 0:
                print(f"  {success}/{len(new_files)}...")

    print(f"Successfully processed {success}/{len(new_files)} new files")

    write_time_series(csv_path, by_minute)
    write_events_csv(events_csv_path, by_minute, top_n=50)
    write_components_csv(components_csv_path, by_minute)
    write_summary(summary_path, by_minute, totals, success)

    save_data(log_path, by_minute, totals, processed_set)

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
