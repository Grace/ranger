#!/usr/bin/env bash
# Run 3. Fresh baseline per case, and injections verified from the captured
# windows rather than from container logs.
#
# Run 2 captured one baseline and compared it against windows up to 32 minutes
# later. The ad service drifted 2.5x over that span with nothing injected into
# it, so `ad · GetAds` topped rankings in windows where ad was untouched. That
# confound was larger than the result it confounded, which is why run 2's
# accuracy table is a floor on the error rate rather than an estimate of it.
#
# Two changes:
#
#   1. Every case gets its own baseline, captured minutes before its own
#      incident window rather than half an hour before.
#
#   2. Injection verification reads the two windows instead of grepping
#      container logs, and checks the service the case is actually about. Run 2
#      verified adManualGc with `docker logs ad | grep -c "Manual GC"`, got
#      zero, and recorded "injection did NOT fire" for a case where the ad
#      service's own work had gone from 4.2ms to 2.1s. It also credited
#      recommendationCacheFailure with "185 error spans" that belonged to
#      frontend, frontend-proxy and frontend-web — a whole-file grep, not a
#      per-service one.
#
# cartFailure and intlShippingSlowdown stay out: cartFailure only fires inside
# EmptyCart (~3 calls/min, cannot clear min-samples in five minutes), and
# intlShippingSlowdown delays non-US addresses only, of which one of nine
# load-generator personas qualifies.
set -uo pipefail
cd "$(dirname "$0")"

WIN=${WIN:-300}          # capture window, seconds
SETTLE=${SETTLE:-90}     # quiet time before a baseline
SOAK=${SOAK:-60}         # time for an injection to take effect before capturing
RESET=${RESET:-45}       # quiet time after a case
OUT=${OUT:-run3}
VERIFY=${VERIFY:-./verify-injection.py}
CAP=./capture.sh
LOG=$OUT/run3.log

mkdir -p "$OUT"
: > "$LOG"

# service and operation substring to verify each case against. The point is to
# check the service the case claims to be about, not whatever moved.
declare -a CASES=(
  "adHighCpu|ad|GetAds"
  "adManualGc|ad|GetAds"
  "productCatalogFailure|product-catalog|"
  "recommendationCacheFailure|recommendation|"
)

setflag() { python3 - "$1" "$2" <<'PY'
import json, pathlib, sys
name, variant = sys.argv[1], sys.argv[2]
p = pathlib.Path("src/flagd/demo.flagd.json")
d = json.loads(p.read_text())
f = d["flags"][name]
f["defaultVariant"] = variant
t = f.get("targeting")
# A targeting rule overrides defaultVariant, so setting the default alone
# leaves a flag disabled while appearing to work. That is how
# productCatalogFailure silently never fired for two runs.
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

say() { echo "=== $(date +%H:%M:%S) $* ===" | tee -a "$LOG"; }

say "run 3 start · win=${WIN}s settle=${SETTLE}s soak=${SOAK}s reset=${RESET}s out=$OUT"

for entry in "${CASES[@]}"; do
  IFS='|' read -r flag svc op <<< "$entry"

  say "$flag · quiet, then fresh baseline"
  allflagsoff
  sleep "$SETTLE"
  $CAP "$WIN" "$OUT/baseline-$flag.jsonl" 2>&1 | tee -a "$LOG"

  say "$flag -> on"
  setflag "$flag" on
  sleep "$SOAK"
  $CAP "$WIN" "$OUT/incident-$flag.jsonl" 2>&1 | tee -a "$LOG"

  # Verified before the flag goes back off, and from the windows themselves.
  python3 "$VERIFY" "$OUT/baseline-$flag.jsonl" "$OUT/incident-$flag.jsonl" "$svc" "$op" 2>&1 | tee -a "$LOG"

  allflagsoff
  sleep "$RESET"
done

say "run 3 done, all flags off"

# The whole reason for run 3: baselines taken minutes apart should agree. If
# ad · GetAds still drifts across them, the drift is not baseline staleness and
# the README needs a different explanation rather than a better number.
say "baseline drift check — ad · GetAds p50 across the four fresh baselines"
for entry in "${CASES[@]}"; do
  IFS='|' read -r flag _ _ <<< "$entry"
  python3 - "$OUT/baseline-$flag.jsonl" "$flag" <<'PY' | tee -a "$LOG"
import json, statistics, sys
path, label = sys.argv[1], sys.argv[2]
d = []
for line in open(path):
    line = line.strip()
    if not line:
        continue
    try:
        b = json.loads(line)
    except Exception:
        continue
    for rs in b.get("resourceSpans", []):
        at = {a["key"]: a.get("value", {}) for a in rs.get("resource", {}).get("attributes", [])}
        if at.get("service.name", {}).get("stringValue") != "ad":
            continue
        for ss in rs.get("scopeSpans", []):
            for sp in ss.get("spans", []):
                if "GetAds" not in sp.get("name", ""):
                    continue
                try:
                    d.append((int(sp["endTimeUnixNano"]) - int(sp["startTimeUnixNano"])) / 1e6)
                except Exception:
                    pass
print(f"  baseline-{label}: ad · GetAds p50 {statistics.median(d):.2f}ms  (n={len(d)})" if d else f"  baseline-{label}: no ad·GetAds spans")
PY
done

ls -lh "$OUT"/*.jsonl | tee -a "$LOG"
