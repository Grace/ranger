# ranger

Deterministic root-cause localization for distributed systems.

Given an incident window and a symptom, ranger identifies the service and
deploy most likely responsible — with the evidence and the scoring that
produced the ranking, or an explicit "no code change explains this."

## Design

The causal engine is deterministic. It walks the OpenTelemetry trace DAG to
find the **deepest span whose deviation from baseline is not explained by its
children**. Resource attributes such as `service.version` are carried through
so a result can name the deploy it came from; resolving that to a commit range
and a diff is designed but **not built** — see [Not built](#not-built).

An LLM is optional, and when enabled it narrates a ranking it did not produce:
the answer exists before the model is called, the model is handed the finished
ranking rather than the traces, and its prose is checked against that ranking
before anyone sees it. Prose that names a different service is discarded and
the deterministic summary is printed instead. See
[Narration](#narration).

Two properties follow, and neither is available to an LLM-first design:

- **Reproducible** — the same incident yields the same answer.
- **Auditable** — the inputs and the scoring are shown.

## Status

Pre-alpha. Scored against four labeled failures in the OpenTelemetry Demo,
ranking 88 operations per window: **top-1 25%, and it names the wrong service
in a quarter to a half of cases depending on the ranking mode.** Chance on 88
operations is roughly 1%, which is the only reason 25% is worth reporting at
all rather than the reason it is good. See [Accuracy](#accuracy), which also describes
a drift confound in the harness large enough that the number should be read as a
floor on the error rate rather than an estimate of it.

`ranger localize` reads two windows of OTLP/JSON from the OpenTelemetry
Collector's file exporter, ranks operations, and writes a self-contained HTML
report. `ranger eval` scores a manifest of labeled incidents. `deploy/` has
the runbook for producing those windows from the OpenTelemetry Demo.

## Accuracy

Against the OpenTelemetry Demo at commit `8c47d47`, four labeled failures, one
300-second baseline and one 300-second window per case, `min-samples 20`.

| | top-1 | top-3 | wrong | declined |
| --- | --- | --- | --- | --- |
| `-rank deviation` | 25% | 25% | **50%** | 25% |
| `-rank deviation -exclude load-generator` | 25% | 25% | **50%** | 25% |
| `-rank effect` | 25% | 50% | 25% | 25% |
| `-rank effect -exclude load-generator` | 25% | 50% | 25% | 25% |

Four cases is not a benchmark. It is enough to say that ranger names the wrong
service more often than the right one, and that is the number to carry.

Per case, under `-rank effect`:

| case | injection verified | outcome | rank of the responsible service |
| --- | --- | --- | --- |
| `adManualGc` | yes — GetAds p50 4.33ms → 2124.12ms | **correct** | 1 |
| `recommendationCacheFailure` | yes — 185 error spans | in top 3 | 2 |
| `adHighCpu` | yes — GetAds p50 4.33ms → 7.81ms | declined | 5 |
| `productCatalogFailure` | yes — 1087 error spans | **wrong** | 7 |

### The confound, which is larger than the result

A single baseline was captured at the start of the run and compared against
windows up to 32 minutes later. The ad service drifted over that span with
nothing injected into it:

```
baseline                     GetAds p50   4.33ms
adHighCpu                    GetAds p50   7.81ms   ← injected
adManualGc                   GetAds p50 2124.12ms  ← injected
productCatalogFailure        GetAds p50  11.19ms   ← ad untouched
recommendationCacheFailure   GetAds p50  10.42ms   ← ad untouched
```

The ad service ends the run roughly 2.5× slower than it began it. That drift is
attributed to whichever flag happened to be on, which is why `ad · GetAds` tops
the ranking in the `recommendationCacheFailure` window — a false positive
manufactured by the harness, not by the ranking.

**So the honest reading is that this measures the harness at least as much as it
measures ranger.** The fix is interleaving a fresh baseline between injections
rather than reusing one, and until that runs, the table above is a floor on the
error rate and not an estimate of it.

### The distinction the whole thing exists for

`adManualGc` is the case that shows why this is done over the trace DAG rather
than over a flat set of events. Both of these are the *same call* — the ad
service's server span and the frontend's client span for it:

```
ad · oteldemo.AdService/GetAds          slower    self +2.096s   duration +2.098s
frontend · oteldemo.AdService/GetAds    waiting   self +8.97ms   duration +2.125s
```

The caller's duration moved roughly 237× more than its own work did. On a
duration ranking the two are indistinguishable and the caller may well sort
higher; self time separates them, and the caller is scored zero and never
promoted to an answer. Reproducible from `baseline.jsonl` and
`incident-adManualGc.jsonl`.

An earlier window measured the same effect at z 409.99 with a runner-up of 0.52.
Its capture files were deleted, so the ranking was recovered from the report
payload and checked in at
[`evidence/adManualGc-ranking.json`](evidence/adManualGc-ranking.json) rather
than quoted from nothing. The run above supersedes it and is reproducible; the
artifact is kept because figures from that window appear in the write-up.

### What the two ranking modes are

`-rank deviation` scores an operation by robust-z: how far its self-time shift
falls outside its own historical spread. That is a significance test, and
ranger originally read it as importance. Across tiers those diverge badly — a
load generator moving 30% of a 2.4-second baseline outscores a gRPC handler
tripling 3.5ms — so `-rank effect` scores the shift as a fraction of the
operation's own baseline instead, an effect size, with a threshold of 1.0 (the
operation doubled its own work) in place of 3.0 deviations. Both keep the 2ms
absolute floor, so a 40µs cache hit going to 200µs cannot be promoted by ratio
alone.

Effect-size ranking was designed while looking at one window and then measured
on four, of which three were not examined first. It halves the wrong rate and
doubles top-3 without moving top-1. That is a modest result on a small set, and
the default is still `deviation` until a run without the drift confound says
otherwise.

### Excluding the load generator changes almost nothing

It was worth checking, because a load generator's spans are the test harness
rather than the system under test, and under deviation ranking they did reach
ranks 2 and 3. But the headline numbers are identical with and without the
exclusion in both modes. Every published figure states what was excluded, and
`Result.Excluded` carries the list back out so a caller cannot omit it by
accident.

### Cases that are not in the set, and why

`cartFailure` only fires inside `EmptyCart`, which sees roughly three calls a
minute at demo load — 14 samples in the incident window against a floor of 20.
It cannot clear `min-samples` in five minutes, so it was removed before scoring
rather than kept as a guaranteed decline.

`intlShippingSlowdown` looked like the cleanest case on paper and is not usable:
it delays non-US addresses only, and one of the nine load-generator personas is
Canadian. That is a tail effect on an already-rare operation, not a shift.

`productCatalogFailure` could not fire at all until this run. The demo ships it
with a targeting rule whose branches are **both** `"off"`, and a targeting rule
overrides `defaultVariant` — so flipping the default, which is what the harness
did, left the flag permanently disabled while appearing to work. An earlier
scoring run counted it as a decline. That was the harness, and it is the reason
every case now verifies its own injection and records the evidence.

## Narration

`-narrate <openai-compatible-url>` writes the ranking up in prose for a pager
message or an incident channel, where a table of robust-z scores is the wrong
shape. `RANGER_NARRATE_ENDPOINT`, `RANGER_NARRATE_MODEL` and
`RANGER_NARRATE_KEY` set the same things from the environment. Off by default:
ranger's answer does not depend on a network call, and a tool that localizes an
incident should not stop working because a model provider is having one.

The ordering is the design. A model that reads spans and names a culprit is
guessing with extra steps — it cannot be reproduced, it cannot be audited, and
when it is wrong it is wrong fluently. Here the ranking is produced first, the
model is given only the finished ranking, and the prose is checked against it
before it is shown. Two failures are rejected:

- Prose that names a service other than the one the engine localized, or that
  leads with a service the engine called *waiting on something below it*. That
  is the caller/callee confusion the whole DAG walk exists to resolve, and it is
  the mistake a narrator repeats most readily, because both spans really did get
  slower.
- Prose that supplies a cause at all when the engine declined to localize.
  Turning "nothing here explains it" into a service name is how someone gets
  woken up for the wrong thing.

The model never sees a span, a trace id, or request content — only operation
names, verdicts and the measured shifts. That is all it needs to write a
paragraph, and trace payloads carry customer data while a narration endpoint is
somebody else's server.

## Not built

- **Build provenance to a commit range.** `service.version` and
  `deployment.environment` are carried through from resource attributes, so a
  result can say which build it saw. Resolving a version to a commit range and
  a diff needs a source of build metadata and is not implemented.
- **A fresh baseline per case in the harness.** This is the drift confound
  described above and the reason the accuracy table is a floor rather than an
  estimate.

## License

MIT
