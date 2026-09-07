#!/usr/bin/env bash
# Score run 2 across both ranking modes and both exclusion settings.
#
# Four numbers, not one. The ranking mode is a design choice and the exclusion
# is a judgment call, and a single headline figure hides which of the two is
# doing the work.
set -uo pipefail
cd "$(dirname "$0")"
BIN=/tmp/ranger
(cd ~/code/ranger && go build -o "$BIN" ./cmd/ranger) || { echo "build failed"; exit 1; }

for rank in deviation effect; do
  for excl in "" "load-generator"; do
    if [ -z "$excl" ]; then label="no exclusions"; args=(); else label="excluding $excl"; args=(-exclude "$excl"); fi
    echo "=============================================================="
    echo "  rank=$rank · $label"
    echo "=============================================================="
    "$BIN" eval -cases cases.json -rank "$rank" ${args[@]+"${args[@]}"}
    echo
  done
done
