#!/usr/bin/env python3
"""
Aggregate JSON log summaries into time-series format.
Supports incremental updates by tracking processed files.

Usage:
    # One-time full aggregation
    python3 scripts/aggregate_logs.py logs/20251105/0750_heavy-load-5jobs-per-3min/

    # Watch mode - continuously process new files
    python3 scripts/aggregate_logs.py logs/20251105/0750_heavy-load-5jobs-per-3min/ --watch
"""

import json
import sys
import time
from pathlib import Path
from collections import defaultdict
from datetime import datetime
from zoneinfo import ZoneInfo

STATE_FILE_NAME = ".aggregate_state.json"

def load_state(log_dir):
    """Load processing state (which files have been processed)."""
    state_file = log_dir / STATE_FILE_NAME
    if state_file.exists():
        try:
            with open(state_file) as f:
                return json.load(f)
        except Exception as e:
            print(f"Warning: Could not load state file: {e}", file=sys.stderr)
    return {'processed_files': [], 'last_update': None}

def save_state(log_dir, state):
    """Save processing state."""
    state_file = log_dir / STATE_FILE_NAME
    melbourne_tz = ZoneInfo("Australia/Melbourne")
    state['last_update'] = datetime.now(melbourne_tz).isoformat()
    with open(state_file, 'w') as f:
        json.dump(state, f, indent=2)

def load_existing_data(csv_path, messages_csv_path):
    """Load existing CSV data into memory."""
    by_minute = defaultdict(lambda: {
        'level_counts': defaultdict(int),
        'message_counts': defaultdict(int),
        'component_counts': defaultdict(int),
        'samples': 0,
        'total_lines': 0,
        'failed_to_parse': 0
    })

    # Load level counts from time_series.csv
    if csv_path.exists():
        try:
            with open(csv_path) as f:
                next(f)  # Skip header
                for line in f:
                    parts = line.strip().split(',')
                    if len(parts) >= 7:
                        timestamp = parts[0]
                        by_minute[timestamp]['samples'] = int(parts[1])
                        by_minute[timestamp]['total_lines'] = int(parts[2])
                        by_minute[timestamp]['level_counts']['info'] = int(parts[3])
                        by_minute[timestamp]['level_counts']['warn'] = int(parts[4])
                        by_minute[timestamp]['level_counts']['error'] = int(parts[5])
                        by_minute[timestamp]['level_counts']['debug'] = int(parts[6])
        except Exception as e:
            print(f"Warning: Could not load existing CSV: {e}", file=sys.stderr)

    # Load message counts from messages_per_minute.csv
    if messages_csv_path.exists():
        try:
            import csv
            with open(messages_csv_path) as f:
                reader = csv.DictReader(f)
                for row in reader:
                    timestamp = row.pop('timestamp')
                    for message, count_str in row.items():
                        count = int(count_str)
                        if count > 0:
                            by_minute[timestamp]['message_counts'][message] = count
        except Exception as e:
            print(f"Warning: Could not load existing messages CSV: {e}", file=sys.stderr)

    return by_minute

def process_json_file(json_file, by_minute, all_messages):
    """Process a single JSON file and update aggregation data."""
    try:
        with open(json_file) as f:
            data = json.load(f)

        # Extract metadata
        meta = data.get('meta', {})
        total_lines = meta.get('total_lines', 0)
        failed_to_parse = meta.get('failed_to_parse', 0)

        # Process level counts
        for timestamp, levels in data.get('level_counts', {}).items():
            minute_key = timestamp[:16]  # YYYY-MM-DDTHH:MM

            by_minute[minute_key]['samples'] += 1
            by_minute[minute_key]['total_lines'] += total_lines
            by_minute[minute_key]['failed_to_parse'] += failed_to_parse

            for level, count in levels.items():
                by_minute[minute_key]['level_counts'][level] += count

        # Process message counts
        for timestamp, messages in data.get('message_counts', {}).items():
            minute_key = timestamp[:16]

            for msg_data in messages:
                message = msg_data.get('message', 'unknown')
                count = msg_data.get('count', 0)
                by_minute[minute_key]['message_counts'][message] += count
                all_messages[message] += count

        # Process component counts (new field from slog migration)
        for timestamp, components in data.get('component_counts', {}).items():
            minute_key = timestamp[:16]
            for component, count in components.items():
                by_minute[minute_key]['component_counts'][component] += count

        return True
    except Exception as e:
        print(f"Error processing {json_file}: {e}", file=sys.stderr)
        return False

def write_csv(csv_path, by_minute):
    """Write aggregated data to CSV."""
    with open(csv_path, 'w') as f:
        f.write("timestamp,samples,total_lines,info,warn,error,debug\n")
        for minute in sorted(by_minute.keys()):
            data = by_minute[minute]
            levels = data['level_counts']
            f.write(f"{minute},{data['samples']},{data['total_lines']},"
                   f"{levels.get('info', 0)},{levels.get('warn', 0)},"
                   f"{levels.get('error', 0)},{levels.get('debug', 0)}\n")

def write_messages_csv(csv_path, by_minute, top_n=20):
    """Write messages per minute to CSV.

    Args:
        csv_path: Path to write the CSV file
        by_minute: Dictionary of minute data with message_counts
        top_n: Number of top messages to include as columns (default: 20)
    """
    # Collect all messages and their total counts
    all_message_totals = defaultdict(int)
    for data in by_minute.values():
        for message, count in data['message_counts'].items():
            all_message_totals[message] += count

    # Get top N messages by total count
    top_messages = sorted(all_message_totals.items(), key=lambda x: x[1], reverse=True)[:top_n]
    top_message_names = [msg for msg, _ in top_messages]

    # Write CSV
    with open(csv_path, 'w') as f:
        # Header: timestamp + message names
        header = "timestamp," + ",".join(f'"{msg}"' for msg in top_message_names)
        f.write(header + "\n")

        # Data rows
        for minute in sorted(by_minute.keys()):
            data = by_minute[minute]
            counts = [str(data['message_counts'].get(msg, 0)) for msg in top_message_names]
            f.write(f"{minute}," + ",".join(counts) + "\n")

def write_components_csv(csv_path, by_minute):
    """Write per-minute component log volume to CSV."""
    # Collect all component names seen across all minutes.
    all_components: set = set()
    for data in by_minute.values():
        all_components.update(data['component_counts'].keys())
    components = sorted(all_components)

    with open(csv_path, 'w') as f:
        f.write("timestamp," + ",".join(components) + "\n")
        for minute in sorted(by_minute.keys()):
            counts = [str(by_minute[minute]['component_counts'].get(c, 0)) for c in components]
            f.write(f"{minute}," + ",".join(counts) + "\n")


def write_summary(summary_path, by_minute, all_messages, new_files_count):
    """Write markdown summary."""
    total_samples = sum(m['samples'] for m in by_minute.values())
    total_lines = sum(m['total_lines'] for m in by_minute.values())
    total_failed = sum(m['failed_to_parse'] for m in by_minute.values())

    melbourne_tz = ZoneInfo("Australia/Melbourne")
    now = datetime.now(melbourne_tz)

    with open(summary_path, 'w') as f:
        f.write("# Log Aggregation Summary\n\n")
        f.write(f"**Generated:** {now.isoformat()}\n\n")
        f.write(f"**New files processed:** {new_files_count}\n\n")

        if by_minute:
            f.write(f"**Time range:** {min(by_minute.keys())} to {max(by_minute.keys())}\n\n")

        f.write("## Overall Statistics\n\n")
        f.write(f"- Total samples: **{total_samples}**\n")
        f.write(f"- Total log lines: **{total_lines:,}**\n")
        f.write(f"- Failed to parse: **{total_failed}**\n")
        f.write(f"- Parse success rate: **{100 * (1 - total_failed / max(total_lines, 1)):.1f}%**\n\n")

        # Log levels by minute
        f.write("## Log Levels by Minute (Last 20)\n\n")
        f.write("| Timestamp | Samples | Lines | Info | Warn | Error | Debug |\n")
        f.write("|-----------|---------|-------|------|------|-------|-------|\n")

        for minute in sorted(by_minute.keys())[-20:]:
            data = by_minute[minute]
            levels = data['level_counts']
            f.write(f"| {minute} | {data['samples']} | {data['total_lines']} | "
                   f"{levels.get('info', 0)} | {levels.get('warn', 0)} | "
                   f"{levels.get('error', 0)} | {levels.get('debug', 0)} |\n")

        # Top messages
        f.write("\n## Top 20 Messages (Overall)\n\n")
        f.write("| Count | Message |\n")
        f.write("|-------|----------|\n")

        top_messages = sorted(all_messages.items(), key=lambda x: x[1], reverse=True)[:20]

        for msg, count in top_messages:
            # Escape pipe characters in messages for markdown tables
            escaped_msg = msg[:70].replace('|', '\\|')
            f.write(f"| {count:,} | {escaped_msg} |\n")

        # Log volume by component
        f.write("\n## Log Volume by Component\n\n")
        all_component_totals: dict = defaultdict(int)
        for data in by_minute.values():
            for component, count in data['component_counts'].items():
                all_component_totals[component] += count

        if all_component_totals:
            f.write("| Component | Total Logs |\n")
            f.write("|-----------|------------|\n")
            for component, count in sorted(all_component_totals.items(), key=lambda x: -x[1]):
                f.write(f"| {component} | {count:,} |\n")
        else:
            f.write("_No component data — logs may predate slog migration._\n")

        # Critical patterns
        f.write("\n## Critical Patterns\n\n")

        critical_keywords = [
            'Emergency scale-down',
            'error',
            'failed',
            'panic',
            'crash',
            'timeout',
            'killed'
        ]

        critical_found = {}
        for keyword in critical_keywords:
            for msg, count in all_messages.items():
                if keyword.lower() in msg.lower():
                    if keyword not in critical_found:
                        critical_found[keyword] = []
                    critical_found[keyword].append((msg, count))

        if critical_found:
            for keyword, findings in critical_found.items():
                f.write(f"\n### '{keyword}' patterns found:\n\n")
                for msg, count in sorted(findings, key=lambda x: x[1], reverse=True)[:5]:
                    escaped_msg = msg[:65].replace('|', '\\|')
                    f.write(f"- **{count:,}x** {escaped_msg}\n")
        else:
            f.write("✅ No critical patterns detected\n")

def aggregate_logs(log_dir, incremental=True):
    """Aggregate JSON summaries, optionally in incremental mode."""
    log_path = Path(log_dir)

    if not log_path.exists():
        print(f"Error: Directory {log_dir} does not exist")
        return False

    csv_path = log_path / "time_series.csv"
    components_csv_path = log_path / "components_per_minute.csv"
    summary_path = log_path / "summary.md"

    # Load state
    state = load_state(log_path) if incremental else {'processed_files': []}
    processed_set = set(state['processed_files'])

    # Find JSON files
    all_json_files = sorted(log_path.glob("*.json"))
    new_files = [f for f in all_json_files if f.name not in processed_set]

    if not new_files:
        if incremental:
            print(f"No new files to process (already processed {len(processed_set)} files)")
            return True
        else:
            print(f"No JSON files found in {log_dir}")
            return False

    # Load existing data if incremental
    messages_csv_path = log_path / "messages_per_minute.csv"
    by_minute = load_existing_data(csv_path, messages_csv_path) if incremental else defaultdict(lambda: {
        'level_counts': defaultdict(int),
        'message_counts': defaultdict(int),
        'component_counts': defaultdict(int),
        'samples': 0,
        'total_lines': 0,
        'failed_to_parse': 0
    })

    all_messages = defaultdict(int)

    # Rebuild all_messages from by_minute (for global totals in summary)
    for data in by_minute.values():
        for message, count in data['message_counts'].items():
            all_messages[message] += count

    # Process only new files (incremental)
    print(f"Processing {len(new_files)} new files...")
    success_count = 0
    for json_file in new_files:
        try:
            with open(json_file) as f:
                data = json.load(f)

            # Extract metadata
            meta = data.get('meta', {})
            total_lines = meta.get('total_lines', 0)
            failed_to_parse = meta.get('failed_to_parse', 0)

            # Process level counts
            for timestamp, levels in data.get('level_counts', {}).items():
                minute_key = timestamp[:16]

                by_minute[minute_key]['samples'] += 1
                by_minute[minute_key]['total_lines'] += total_lines
                by_minute[minute_key]['failed_to_parse'] += failed_to_parse

                for level, count in levels.items():
                    by_minute[minute_key]['level_counts'][level] += count

            # Process message counts
            for timestamp, messages in data.get('message_counts', {}).items():
                minute_key = timestamp[:16]
                for msg_data in messages:
                    message = msg_data.get('message', 'unknown')
                    count = msg_data.get('count', 0)
                    by_minute[minute_key]['message_counts'][message] += count
                    all_messages[message] += count

            # Process component counts
            for timestamp, components in data.get('component_counts', {}).items():
                minute_key = timestamp[:16]
                for component, count in components.items():
                    by_minute[minute_key]['component_counts'][component] += count

            processed_set.add(json_file.name)
            success_count += 1
            if success_count % 10 == 0:
                print(f"  Processed {success_count}/{len(new_files)} files...")
        except Exception as e:
            print(f"Error processing {json_file}: {e}", file=sys.stderr)

    print(f"Successfully processed {success_count}/{len(new_files)} new files")

    # Write outputs
    write_csv(csv_path, by_minute)
    write_messages_csv(messages_csv_path, by_minute, top_n=50)
    write_components_csv(components_csv_path, by_minute)
    write_summary(summary_path, by_minute, all_messages, len(new_files))

    # Save state
    if incremental:
        state['processed_files'] = sorted(list(processed_set))
        save_state(log_path, state)

    print(f"\nOutputs written:")
    print(f"  CSV: {csv_path}")
    print(f"  Messages CSV: {messages_csv_path}")
    print(f"  Components CSV: {components_csv_path}")
    print(f"  Summary: {summary_path}")

    return True

def watch_mode(log_dir, interval=10):
    """Continuously watch for new files and process them."""
    print(f"Watch mode: monitoring {log_dir} (checking every {interval}s)")
    print("Press Ctrl+C to stop\n")

    try:
        while True:
            aggregate_logs(log_dir, incremental=True)
            time.sleep(interval)
    except KeyboardInterrupt:
        print("\n\nWatch mode stopped")

if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description='Aggregate JSON log summaries')
    parser.add_argument('log_dir', help='Directory containing JSON log files')
    parser.add_argument('--watch', action='store_true', help='Watch mode: continuously process new files')
    parser.add_argument('--interval', type=int, default=10, help='Check interval in watch mode (default: 10s)')
    parser.add_argument('--full', action='store_true', help='Full reprocess (ignore state)')

    args = parser.parse_args()

    if args.watch:
        watch_mode(args.log_dir, args.interval)
    else:
        aggregate_logs(args.log_dir, incremental=not args.full)
