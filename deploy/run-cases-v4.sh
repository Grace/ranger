#!/usr/bin/env bash
# Run 4. Eight cases, three repeats each, drawn from the fourteen flags the
# demo ships — the ones that can be scored against a defensible label.
#
# Run 3 measured four cases once and moved top-1 from 25% to 50% under effect
# ranking. That is one case changing. Nothing can be concluded from it, and the
# purpose of this run is to make the next comparison mean something:
#
#   Repeats. Running the same flag three times measures the variance of the
#   measurement itself. Without that number there is no way to tell a real
#   improvement from a different afternoon.
#
#   A control. Every case so far has a culprit, so declining is always a miss
#   and a localizer that never speaks scores the same as one that never
#   notices. loadGeneratorFloodHomepage raises load without breaking anything:
#   the correct answer is silence, and naming a cause is a false positive.
#
#   Graduated severity. paymentFailure and emailMemoryLeak ship with variants,
#   so the same fault can be injected at several magnitudes. That locates the
#   detection floor instead of guessing at it.
#
# Excluded, with reasons. cartFailure fires only inside EmptyCart (~3 calls a
# minute, cannot clear min-samples in five minutes). intlShippingSlowdown
# delays non-US addresses only, and one of nine load-generator personas
# qualifies. failedReadinessProbe and paymentUnreachable remove a service
# rather than degrading it, which is a different question than localization.
#
# Two more were dropped for having no defensible label, which is the failure
# this whole set exists to avoid. kafkaQueueProblems "overloads Kafka queue
# while simultaneously introducing a consumer side delay" — producer and
# consumer are different services and the blast radius covers both, so there is
# no single responsible one. imageSlowLoad is described as "slow loading images
# in the frontend" while the demo ships a separate image-provider service;
# labelling it frontend would name the symptom rather than the cause.
#
# Eight well-labelled cases are worth more than ten with two guesses in them.
set -uo pipefail
cd "$(dirname "$0")"

WIN=${WIN:-300}
SETTLE=${SETTLE:-90}
SOAK=${SOAK:-60}
RESET=${RESET:-45}
REPEATS=${REPEATS:-3}
OUT=${OUT:-run4}
VERIFY=${VERIFY:-./verify-injection.py}
CAP=./capture.sh
LOG=$OUT/run4.log

mkdir -p "$OUT"
: > "$LOG"

# name | flag | variant | verify-service | verify-op | expectService ("" = control)
declare -a CASES=(
  "adHighCpu|adHighCpu|on|ad|GetAds|ad"
  "adManualGc|adManualGc|on|ad|GetAds|ad"
  "adFailure|adFailure|on|ad|GetAds|ad"
  "productCatalogFailure|productCatalogFailure|on|product-catalog||product-catalog"
  "recommendationCacheFailure|recommendationCacheFailure|on|recommendation||recommendation"
  "paymentFailure-25|paymentFailure|25%|payment||payment"
  "emailMemoryLeak-1000x|emailMemoryLeak|1000x|email||email"
  "control-flood|loadGeneratorFloodHomepage|on|frontend||"
)

setflag() { python3 - "$1" "$2" <<'PY'
import json, pathlib, sys
name, variant = sys.argv[1], sys.argv[2]
p = pathlib.Path("src/flagd/demo.flagd.json")
d = json.loads(p.read_text())
f = d["flags"][name]
if variant not in f["variants"]:
    raise SystemExit(f"flag {name} has no variant {variant!r}; has {list(f['variants'])}")
f["defaultVariant"] = variant
t = f.get("targeting")
# A targeting rule overrides defaultVariant, so setting the default alone
# leaves a flag disabled while appearing to work.
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

total=$(( ${#CASES[@]} * REPEATS ))
say "run 4 start · ${#CASES[@]} cases x $REPEATS repeats = $total · ~$(( total * 13 )) min"

for rep in $(seq 1 "$REPEATS"); do
  for entry in "${CASES[@]}"; do
    IFS='|' read -r name flag variant svc op expect <<< "$entry"
    tag="${name}-r${rep}"

    say "$tag · quiet, then fresh baseline"
    allflagsoff
    sleep "$SETTLE"
    $CAP "$WIN" "$OUT/baseline-$tag.jsonl" 2>&1 | tee -a "$LOG"

    say "$tag · $flag -> $variant"
    setflag "$flag" "$variant" 2>&1 | tee -a "$LOG"
    sleep "$SOAK"
    $CAP "$WIN" "$OUT/incident-$tag.jsonl" 2>&1 | tee -a "$LOG"

    # A control is expected to show no shift; that is the point, not a failure,
    # so its verification is recorded but never treated as a problem.
    python3 "$VERIFY" "$OUT/baseline-$tag.jsonl" "$OUT/incident-$tag.jsonl" "$svc" "$op" 2>&1 | tee -a "$LOG"
    if [ -z "$expect" ]; then
      echo "    (control: no shift expected)" | tee -a "$LOG"
    fi

    allflagsoff
    sleep "$RESET"
  done
done

say "run 4 done, all flags off"

# Manifest, written from the same table that drove the captures so the two
# cannot disagree.
python3 - "$OUT" "$REPEATS" "${CASES[@]}" > "$OUT/cases.json" <<'PY'
import json, sys
out, repeats, entries = sys.argv[1], int(sys.argv[2]), sys.argv[3:]
cases = []
for rep in range(1, repeats + 1):
    for e in entries:
        name, flag, variant, svc, op, expect = e.split("|")
        tag = f"{name}-r{rep}"
        c = {
            "name": tag,
            "flag": flag,
            "baselineFile": f"baseline-{tag}.jsonl",
            "incidentFile": f"incident-{tag}.jsonl",
            "notes": f"variant={variant}; repeat {rep} of {repeats}",
        }
        if expect:
            c["expectService"] = expect
        else:
            c["control"] = True
            c["notes"] += "; control — load raised, nothing broken, declining is correct"
        cases.append(c)
print(json.dumps(cases, indent=2))
PY

echo "wrote $OUT/cases.json with $(python3 -c "import json;print(len(json.load(open('$OUT/cases.json'))))") cases" | tee -a "$LOG"
du -sh "$OUT" | tee -a "$LOG"
