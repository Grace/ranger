#!/usr/bin/env python3
"""Verify a fault injection actually fired, without asking ranger.

    verify-injection.py <baseline.jsonl> <incident.jsonl> <service> [op-substring]

Run 2 verified injections by counting a log line — `docker logs ad | grep -c
"Manual GC"` — and it produced a false negative that got written into the case
notes as "injection did NOT fire". It had fired: the ad service's own work went
from 4.2ms to 2.10s in the same window. The log pattern was wrong, not the flag.

So this reads the captured windows instead, and deliberately does not import
anything from ranger. It reports p50 span duration and error-span counts for the
target service, both of which are visible in a single span without reconstructing
the trace DAG. That is weaker than self time — it cannot tell a service that got
slower from one waiting on a slow dependency — but telling those apart is the
claim under test, and a verification step must not depend on the thing it is
checking. For "did the flag change anything", duration and errors are enough,
and they cannot silently disagree with the traces the way a log grep can.

Exit status is 0 when a shift is detected, 1 when nothing moved.
"""
import json
import statistics
import sys


def spans_for(path, service, op_substr):
    """Yield (duration_ms, is_error) for spans of one service."""
    with open(path, "r") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                batch = json.loads(line)
            except json.JSONDecodeError:
                # capture.sh trims partial first/last lines, but a torn line in
                # the middle would be a capture bug worth seeing rather than
                # silently skipping the whole run.
                print(f"  warning: unparseable line in {path}", file=sys.stderr)
                continue
            for rs in batch.get("resourceSpans", []):
                attrs = {a["key"]: a.get("value", {}) for a in rs.get("resource", {}).get("attributes", [])}
                name = attrs.get("service.name", {}).get("stringValue")
                if name != service:
                    continue
                for ss in rs.get("scopeSpans", []):
                    for sp in ss.get("spans", []):
                        if op_substr and op_substr not in sp.get("name", ""):
                            continue
                        try:
                            dur = (int(sp["endTimeUnixNano"]) - int(sp["startTimeUnixNano"])) / 1e6
                        except (KeyError, ValueError):
                            continue
                        # OTLP status code 2 is ERROR. Unset and OK are not.
                        err = sp.get("status", {}).get("code") == 2
                        yield dur, err


def summarize(path, service, op_substr):
    rows = list(spans_for(path, service, op_substr))
    if not rows:
        return None
    durs = sorted(r[0] for r in rows)
    return {
        "n": len(rows),
        "p50": statistics.median(durs),
        "p95": durs[min(len(durs) - 1, int(len(durs) * 0.95))],
        "errors": sum(1 for r in rows if r[1]),
    }


def main():
    if len(sys.argv) < 4:
        print(__doc__, file=sys.stderr)
        return 2
    base_path, inc_path, service = sys.argv[1], sys.argv[2], sys.argv[3]
    op = sys.argv[4] if len(sys.argv) > 4 else ""

    base = summarize(base_path, service, op)
    inc = summarize(inc_path, service, op)

    label = f"{service}" + (f" · {op}" if op else "")
    if base is None or inc is None:
        which = base_path if base is None else inc_path
        print(f"  verify {label}: NO SPANS in {which} — cannot confirm injection")
        return 1

    d_p50 = inc["p50"] - base["p50"]
    d_err = inc["errors"] - base["errors"]
    ratio = (inc["p50"] / base["p50"]) if base["p50"] > 0 else float("inf")

    print(
        f"  verify {label}: "
        f"p50 {base['p50']:.2f}ms -> {inc['p50']:.2f}ms ({ratio:.2f}x, {d_p50:+.2f}ms) · "
        f"errors {base['errors']} -> {inc['errors']} ({d_err:+d}) · "
        f"spans {base['n']} -> {inc['n']}"
    )

    # Either a latency shift worth calling a shift, or errors that were not
    # there before. Thresholds are deliberately loose: this answers "did
    # anything happen", not "how much", and a tight threshold here would
    # reintroduce exactly the false negative it exists to prevent.
    latency_moved = ratio >= 1.5 and d_p50 >= 1.0
    errors_appeared = d_err >= 10
    if latency_moved or errors_appeared:
        why = []
        if latency_moved:
            why.append("latency")
        if errors_appeared:
            why.append("errors")
        print(f"  verify {label}: FIRED ({', '.join(why)})")
        return 0
    print(f"  verify {label}: NO DETECTABLE SHIFT — treat a decline as the demo, not ranger")
    return 1


if __name__ == "__main__":
    sys.exit(main())
