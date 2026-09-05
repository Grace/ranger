#!/usr/bin/env bash
# Capture one window of traces from the collector's file exporter.
#
#   ./capture.sh <seconds> <output.jsonl> [traces.jsonl]
#
# Truncates the exporter's file, waits, then copies what accumulated. The
# collector holds the file O_APPEND, so truncating under it is safe and is the
# simplest way to get a window with clean edges.

set -euo pipefail

secs=${1:?usage: capture.sh <seconds> <output.jsonl> [traces.jsonl]}
out=${2:?usage: capture.sh <seconds> <output.jsonl> [traces.jsonl]}
src=${3:-./inquest-traces/traces.jsonl}

if [[ ! -f $src ]]; then
  echo "capture: $src does not exist — is the collector running with the file exporter?" >&2
  exit 1
fi

: > "$src"
echo "capture: collecting ${secs}s into $out" >&2

# Give the collector's batch processor time to flush the tail of the window.
sleep "$secs"
sleep 3

cp "$src" "$out"
lines=$(wc -l < "$out" | tr -d ' ')
echo "capture: wrote $out ($lines lines)" >&2

if [[ $lines -eq 0 ]]; then
  echo "capture: no spans — check that the load generator is running" >&2
  exit 1
fi
