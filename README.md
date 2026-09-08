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

Pre-alpha. Scored against thirteen labeled incidents in the OpenTelemetry
Demo — eight distinct failures, five of them run twice — ranking roughly 90
operations per window: **top-1 38%, and it names the wrong service in a quarter
to a fifth of cases depending on the ranking mode.** Chance on ninety operations
is near 1%, which is the only reason 38% is worth reporting at all rather than
the reason it is good. See [Accuracy](#accuracy).

`ranger localize` reads two windows of OTLP/JSON from the OpenTelemetry
Collector's file exporter, ranks operations, and writes a self-contained HTML
report. `ranger eval` scores a manifest of labeled incidents. `deploy/` has
the runbook for producing those windows from the OpenTelemetry Demo.

## Accuracy

Against the OpenTelemetry Demo at commit `8c47d47`. Eight labeled failures, five
of them run a second time, for thirteen scored incidents. Each has **its own**
300-second baseline captured minutes before its own 300-second incident window,
`min-samples 20`.

| | top-1 | top-3 | wrong | declined |
| --- | --- | --- | --- | --- |
| `-rank deviation` | 38% | 38% | **38%** | 23% |
| `-rank deviation -exclude load-generator` | 38% | 38% | 23% | 38% |
| `-rank effect` | 38% | 38% | 23% | 38% |
| `-rank effect -exclude load-generator` | 38% | 38% | 23% | 38% |
| `-rank effect-adjusted` | 38% | 38% | **15%** | 46% |
| `-rank effect-adjusted -exclude load-generator` | 38% | 38% | **15%** | 46% |

Thirteen cases is not a benchmark either. It is enough to say that ranger is
right about a third of the time, wrong about a quarter, and silent the rest —
and that the silence is the half worth having.

Per case, under `-rank effect`:

| case | outcome | rank of the responsible service |
| --- | --- | --- |
| `adManualGc` ×2 | **correct** | 1, 1 |
| `recommendationCacheFailure` ×2 | **correct** | 1, 1 |
| `control-flood` | **correct** — declined, and nothing was broken | — |
| `adFailure` ×2 | declined | 1, 1 |
| `adHighCpu` ×2 | declined | 2, 6 |
| `emailMemoryLeak-1000x` | declined | 13 |
| `paymentFailure-25` | **wrong** — named `ad · GetAds` | — |
| `productCatalogFailure` ×2 | **wrong** — named `frontend · GET /api/recommendations` both times | 6, 6 |

### Dividing out what the whole window did

`-rank effect-adjusted` scores each operation against how far the *typical*
operation moved rather than against zero. If the background drifted, every
operation carries that drift, and removing it leaves what is specific to each.

It has the lowest wrong rate of the three modes at the same top-1, and the case
it fixes is `paymentFailure-25`: plain effect names `ad · GetAds` for a payment
fault, and dividing out the background demotes it below the threshold instead.
**That is one case out of thirteen changing.** It is not significance, and the
mode is not the default.

Two things had to be right before it helped at all, and both were wrong first.

Dividing by a median below 1.0 inflates every operation rather than correcting
one. `adHighCpu`'s windows came in at 0.81× and 0.86× — the background got
*faster* — and the naive version turned two honest declines into a wrong answer
and a near miss. Only a background that slowed is deflated; one that sped up
cannot manufacture a false positive, so there is nothing to remove.

Writing the adjusted score directly also overwrote the absolute floor that stops
an operation being promoted by ratio alone. On the control window that promoted
`ad · getAdsByCategory` for going from 310µs to 632µs — a clean doubling of a
third of a millisecond, on a system where nothing was broken. Adjustment
rescales a finding; it must not create one.

The control case caught both. Before it existed, every incident in the set had a
culprit, so an invented one had nothing to fail against.

### What the distribution says that the median does not

Each candidate also carries a two-sample **KS** statistic and a **Wasserstein**
distance over the retained self-time samples. Neither is ranked on: the numbers
above describe the current ranking, and changing the measurement and the ranking
together would leave any difference unattributable to either.

They disagree with the ranking, and they do not fix it. In `paymentFailure-25`,
`checkout · POST` moved 324ms of earth against a median shift of 8.5ms —
twenty-two times more than the operation ranked first — while the service the
case is labelled with does not appear in the top 25 at all. No statistic over
these two windows was going to find it.

### Every repeat agreed with itself

This is the result that makes the rest of the table readable. Five cases ran
twice and all five produced the same outcome both times, including the two that
fail: `productCatalogFailure` named the same wrong operation on both runs, and
`adFailure` declined on both.

Run-to-run variance is therefore not what separates the ranking modes, which
matters because thirteen cases is small enough that it easily could have been.
It also means the failures are systematic rather than unlucky — worth debugging
rather than re-running.

### The set is uneven, and one run is missing

`ad` accounts for six of the thirteen cases, because the capture run was killed
for memory partway through its second repeat and never reached a third. Three
cases have one observation rather than two. The graduated-severity variants
(`paymentFailure` at other percentages, `emailMemoryLeak` at other multipliers)
were never captured, so the detection floor is still unmeasured.

Two flags the demo ships were deliberately left out for having no defensible
label: `kafkaQueueProblems` degrades producer and consumer, which are different
services, and `imageSlowLoad` is documented as being "in the frontend" while the
demo ships a separate `image-provider`. Labelling either would have named a
symptom rather than a cause.

### A control case, which is what makes "declined" mean anything

`loadGeneratorFloodHomepage` raises load without breaking a service, so the
correct answer is silence. Ranger declined it under both ranking modes.

Before this case existed, every incident in the set had a culprit, so declining
was always a miss — and a localizer that never spoke scored identically to one
that never noticed. Naming a cause here is counted as a false positive, with the
wrong answers, because inventing a culprit on a healthy system is the same
failure as naming the wrong one on a broken one.

### Excluding the load generator now matters

It did not on the earlier four-case set, and that is no longer true. Under
`-rank deviation`, dropping the load generator turns two confident wrong answers
into declines: `adHighCpu` and `paymentFailure-25` were both outranked by
load-generator operations rather than by anything in the service that broke.

Under `-rank effect` the exclusion changes nothing, because effect ranking
already scores those operations below the threshold. That is the same tier
mismatch described below, seen from the other side: a load generator's spans are
the test harness, not the system under test, and only one of the two rankings is
robust to their presence.

Every published figure states what was excluded, and `Result.Excluded` carries
the list back out so a caller cannot omit it by accident.

### The confound this replaced, and what it turned out to be

An earlier run captured one baseline at the start and compared it against
windows up to 32 minutes later. `ad · GetAds` read 4.33ms in that baseline and
7.81ms, 10.42ms and 11.19ms in later windows — including windows where nothing
had been injected into the ad service. Every one of those looked like a
degradation, and `ad · GetAds` topped rankings it had nothing to do with.

Capturing a fresh baseline per case fixed the result, but not for the reason
assumed. Four fresh baselines taken minutes apart, all with every flag off,
read:

```
baseline-recommendationCacheFailure   GetAds p50   7.63ms
baseline-adManualGc                   GetAds p50   8.55ms
baseline-adHighCpu                    GetAds p50   9.08ms
baseline-productCatalogFailure        GetAds p50  11.51ms
```

They still span 1.51×. The service is simply this noisy between quiet periods.
So the original 4.33ms was not the start of a slow drift — it was an
unrepresentative sample taken before the system reached steady state, and every
window compared against it inherited the error.

That distinction matters, because a threshold on "how much did the typical
operation move" was tried as a way to detect the bad case and removed: any bar
low enough to catch it sits inside ordinary variance. The calibrated version is
a permutation test over the ranking itself, which adapts to the window instead
of guessing at a constant.

**The earlier table measured the harness at least as much as it measured
ranger.** The one above does not: every case carries its own baseline. What it
still cannot do is distinguish a real improvement from a different afternoon
across only thirteen cases — the repeats say run-to-run variance is small, not
that the sample is large.

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

Effect-size ranking was designed while looking at one window, then measured on
four, then on thirteen. On the larger set the two modes reach the same top-1;
what differs is where the misses go. Effect converts confident wrong answers
into declines — 23% wrong against deviation's 38% — and does it consistently,
with every repeated case agreeing with itself.

That is the property worth having at three in the morning, so **`-rank effect`
is what to use**, and `deviation` remains the compiled-in default only until the
set is large enough to justify moving it. Deviation's extra wrong answers are
not random: they are load-generator operations outranking the service that
actually broke, which is the tier mismatch described above.

### Cases that are not in the set, and why

`cartFailure` only fires inside `EmptyCart`, which sees roughly three calls a
minute at demo load — 14 samples in the incident window against a floor of 20.
It cannot clear `min-samples` in five minutes, so it was removed before scoring
rather than kept as a guaranteed decline.

`intlShippingSlowdown` looked like the cleanest case on paper and is not usable:
it delays non-US addresses only, and one of the nine load-generator personas is
Canadian. That is a tail effect on an already-rare operation, not a shift.

`kafkaQueueProblems` and `imageSlowLoad` were dropped for having no defensible
label. The first "overloads Kafka queue while simultaneously introducing a
consumer side delay" — producer and consumer are different services and the
blast radius covers both. The second is documented as slow images "in the
frontend" while the demo ships a separate `image-provider`; calling it frontend
would label the symptom rather than the cause, which is the error this set
exists to avoid.

`productCatalogFailure` could not fire at all until an earlier run. The demo ships it
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
- **A detection floor.** `paymentFailure` and `emailMemoryLeak` ship graduated
  variants, so the same fault can be injected at several magnitudes. Only one
  magnitude of each was captured, so how small a fault ranger can still find is
  unmeasured.
- **A third repeat, and a balanced set.** `ad` is six of the thirteen cases
  because the capture run was killed for memory partway through its second pass.

## License

MIT
