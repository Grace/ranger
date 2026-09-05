# evidence

Artifacts behind numbers quoted elsewhere in this repository, so a reader can
check a claim without rerunning the demo.

## `adManualGc-ranking.json`

The full 88-row ranking from the `adManualGc` window quoted in the root README
and in the write-up: `ad · oteldemo.AdService/GetAds`, self time 6.963ms →
1893.08ms, robust-z 409.99, runner-up 0.52.

**The capture files this was computed from no longer exist.** `clean-baseline.jsonl`
and `clean-incident-adManualGc.jsonl` were deleted; this JSON was recovered from
the payload embedded in the generated report, which is why it is checked in.
The numbers are the ones the tool produced, but they cannot be regenerated from
raw spans, and that is a weaker form of evidence than a rerun. It is recorded
here rather than quietly relied on.

Two later attempts to reproduce the window failed: the demo's `adManualGc` flag
logged zero collections in the ad service on both runs, with GetAds p50 moving
4.4ms → 4.3ms and 4.26ms. The injection is intermittent in this environment.
The correct reading is that the measurement happened and the conditions for it
have not recurred — not that the effect is confirmed.

The row that carries the argument is not the top one:

```
frontend · oteldemo.AdService/GetAds
  verdict  waiting on something below it   (score 0)
  self     +3.586ms
  duration +1885.559ms
```

The caller's outbound span for the identical call. Its duration moved five
hundred times more than its own work did, which is what a ranking built on
duration cannot separate from a cause.
