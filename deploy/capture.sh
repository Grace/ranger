#!/usr/bin/env bash
# Capture one window of traces from the collector's file exporter.
#
#   ./capture.sh <seconds> <output.jsonl> [traces.jsonl]
#
# Records the exporter file's size, waits, then takes only the bytes that
# arrived in between.
#
# It does NOT truncate the file. Truncating one that the collector holds open
# with O_APPEND does not reset the collector's write offset: the next write
# lands at the old offset and the kernel fills everything before it with NUL
# bytes. The window then begins with megabytes of zeros and no JSON parser
# will touch it. This script was written the wrong way first, and that is the
# failure it produced.

set -euo pipefail

secs=${1:?usage: capture.sh <seconds> <output.jsonl> [traces.jsonl]}
out=${2:?usage: capture.sh <seconds> <output.jsonl> [traces.jsonl]}
src=${3:-./inquest-traces/traces.jsonl}

# Under about five minutes the low-volume services — payment, shipping — do not
# accumulate enough spans to clear min-samples, and their cases score as
# "declined" for want of data rather than for anything inquest did.
if (( secs < 300 )); then
  echo "capture: warning: ${secs}s is short; payment and shipping may not reach min-samples" >&2
fi

if [[ ! -f $src ]]; then
  echo "capture: $src does not exist — is the collector running with the file exporter?" >&2
  exit 1
fi

filesize() {
  # macOS and GNU stat disagree on flags.
  stat -f %z "$1" 2>/dev/null || stat -c %s "$1"
}

start=$(filesize "$src")
echo "capture: collecting ${secs}s into $out (from byte $start)" >&2

sleep "$secs"
sleep 3  # let the batch processor flush the tail of the window

# +N is 1-based, so start+1 is the first byte after the mark. Both ends of the
# window can land mid-line: the mark is a byte offset taken while the collector
# may be mid-write, and the last line may be half-flushed when the window
# closes. Drop a first line that does not begin a JSON object and a last line
# that does not end one. Everything between them is whole.
tail -c "+$((start + 1))" "$src" \
  | awk '
      BEGIN { prev = ""; first = 1 }
      {
        if (first) { first = 0; if ($0 !~ /^[[:space:]]*\{/) next }
        if (prev != "") print prev
        prev = $0
      }
      END { if (prev ~ /\}[[:space:]]*$/) print prev }
    ' \
  > "$out"

lines=$(wc -l < "$out" | tr -d ' ')
echo "capture: wrote $out ($lines lines)" >&2

if [[ $lines -eq 0 ]]; then
  echo "capture: no spans — check that the load generator is running" >&2
  exit 1
fi
