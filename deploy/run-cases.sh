#!/usr/bin/env bash
# The capture driver behind the published accuracy number. An earlier run is
# not comparable, because setflag only
# wrote defaultVariant, which a targeting rule overrides, so
# productCatalogFailure never fired at all. cartFailure is not in this set —
# it only fires inside EmptyCart, which sees ~3 calls a minute at this load
# and cannot clear min-samples in a 5-minute window.
#
# Every case here verifies its own injection AFTER the window and writes the
# evidence to the log, so a decline can be attributed to inquest or to the
# demo without guessing.
set -uo pipefail
cd "$(dirname "$0")"
CAP=./capture.sh
WIN=${WIN:-300}
LOG=./run2.log
: > $LOG

setflag() { python3 - "$1" "$2" <<'PY'
import json, pathlib, sys
name, variant = sys.argv[1], sys.argv[2]
p = pathlib.Path("src/flagd/demo.flagd.json")
d = json.loads(p.read_text())
f = d["flags"][name]
f["defaultVariant"] = variant
t = f.get("targeting")
if isinstance(t, dict) and "if" in t and len(t["if"]) >= 2:
    t["if"][1] = variant
p.write_text(json.dumps(d, indent=2) + "\n")
PY
}

allflagsoff() { python3 - <<'PY'
import json, pathlib
p = pathlib.Path("src/flagd/demo.flagd.json")
d = json.loads(p.read_text())
for k, v in d["flags"].items():
    if "off" not in v["variants"]:
        continue
    v["defaultVariant"] = "off"
    t = v.get("targeting")
    if isinstance(t, dict) and "if" in t and len(t["if"]) >= 2:
        t["if"][1] = "off"
p.write_text(json.dumps(d, indent=2) + "\n")
PY
}

say() { echo "=== $(date +%H:%M:%S) $* ===" | tee -a $LOG; }

say "run 2 start; all flags off, warming 90s"
allflagsoff; sleep 90

say "baseline"
$CAP $WIN baseline.jsonl 2>&1 | tee -a $LOG

for flag in adHighCpu adManualGc productCatalogFailure recommendationCacheFailure; do
  say "$flag -> on"
  setflag "$flag" on
  sleep 60
  gc_before=$(docker logs ad 2>&1 | grep -c "Manual GC" || true)
  $CAP $WIN "incident-$flag.jsonl" 2>&1 | tee -a $LOG
  # Evidence the injection actually fired, recorded before the flag goes back off.
  errs=$(grep -o '"code":2' "incident-$flag.jsonl" | wc -l | tr -d ' ')
  gc_after=$(docker logs ad 2>&1 | grep -c "Manual GC" || true)
  echo "    verify $flag: error-spans=$errs  ad-GC-lines=$((gc_after - gc_before))" | tee -a $LOG
  allflagsoff
  sleep 45
done

say "run 2 done, all flags off"
ls -lh baseline.jsonl incident-*.jsonl | tee -a $LOG
