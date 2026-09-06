# Running inquest against the OpenTelemetry Demo

`inquest demo` proves the plumbing works and nothing else — the generator picks
the answer there, so finding it means nothing. This directory is how you get a
number that counts: the [OpenTelemetry
Demo](https://github.com/open-telemetry/opentelemetry-demo) ships feature flags
that inject specific failures, so ground truth comes from the person who
flipped the flag rather than from inquest.

Everything below was run against demo commit `8c47d47` on 2026-09-05.

## Before you start

The images are about 10 GB. `df -h ~` and `docker system df` before you pull.

## 1. Bring up the demo with a file exporter

```
git clone --depth 1 https://github.com/open-telemetry/opentelemetry-demo.git
cd opentelemetry-demo
mkdir -p inquest-traces
cp /path/to/inquest/deploy/otelcol-config-extras.yml src/otel-collector/
cp /path/to/inquest/deploy/compose.extras.yaml .
docker compose -f compose.yaml -f compose.extras.yaml up -d
```

`compose.extras.yaml` and `src/otel-collector/otelcol-config-extras.yml` are
the demo's own extension seams — both ship as empty stubs meant to be
overwritten, so nothing upstream is being patched.

**The collector merges config files but replaces arrays rather than appending.**
The traces pipeline in the extras file therefore repeats the upstream
exporters. For this checkout they are `debug, span_metrics` running the base
compose, plus `otlp_grpc/jaeger` if you layer in `compose.observability.yaml`.
Check `src/otel-collector/otelcol-config.yml` before copying anything: the
names changed from what older documentation says, and getting this wrong
silently disables the demo's own exporters rather than erroring.

### If the collector crash-loops

The `docker_stats` receiver requests Docker API 1.44. Docker 20.10 caps at
1.41, and the collector treats a receiver that cannot start as fatal, so it
exits and **no traces reach the file at all**:

```
Error response from daemon: client version 1.44 is too new. Maximum supported API version is 1.41
```

The extras config here drops `docker_stats` from the metrics pipeline. It is a
metrics receiver and inquest only reads traces, so nothing is lost. Upgrading
Docker also fixes it.

Confirm spans are landing before going further:

```
wc -l inquest-traces/traces.jsonl
docker ps --filter name=otel-collector --format '{{.Status}}'
```

## 2. Capture a baseline

Let the load generator run a few minutes so the system is warm:

```
/path/to/inquest/deploy/capture.sh 300 baseline.jsonl
```

**Window length is not a detail.** Load is very unevenly distributed across the
demo. In one 30-second window here:

| service | spans |
| --- | --- |
| frontend-proxy | 1734 |
| product-catalog | 718 |
| cart | 205 |
| ad | 50 |
| shipping | 23 |
| payment | 4 |

Browsing dominates; checkout is rare. An operation needs `-min-samples`
observations in **both** windows to be ranked at all, so a short window makes
`payment` and `shipping` cases score as `declined` for lack of data — which
says nothing about the localizer and everything about the capture. Five
minutes is a floor, not a target.

## 3. Inject one failure and capture the incident

Flags live in `src/flagd/demo.flagd.json` and can be flipped in the flagd UI at
`http://localhost:8080/feature`. **Read that file in your own checkout** — the
names moved between demo releases, and a flag the demo does not have produces a
window where nothing happened.

The 14 flags in this checkout:

```
adFailure  adHighCpu  adManualGc  cartFailure  emailMemoryLeak
failedReadinessProbe  imageSlowLoad  intlShippingSlowdown  kafkaQueueProblems
loadGeneratorFloodHomepage  paymentFailure  paymentUnreachable
productCatalogFailure  recommendationCacheFailure
```

Flip exactly one, wait for it to take hold, then:

```
/path/to/inquest/deploy/capture.sh 300 incident-<flag>.jsonl
```

Turn it back off before the next one. Two flags at once produces a window with
two causes and neither the manifest nor the scoring can express that.

### Flags to leave out of the scored set

- **`loadGeneratorFloodHomepage`** — more traffic, no service at fault. There is
  no correct answer to score against. Worth running separately as a check that
  inquest *declines*, which is the right behaviour.
- **`kafkaQueueProblems`** — overloads the queue *and* delays the consumer, so
  the blast radius covers two services.
- **`failedReadinessProbe`**, **`imageSlowLoad`** — the symptom may not appear
  as spans whose self time moves. Run them, but decide ground truth from what
  the traces show, not from the flag name.

Excluding an ambiguous case is honest. Picking whichever service makes the
number look better is not.

## 4. Write down what you know

`cases.example.json` is a starting manifest with the flag names and
`service.name` values verified against this checkout. `expectService` is the
service whose **own work** changed — not where the symptom surfaced. Getting
that backwards publishes a number worse than the tool.

## 5. Score it

```
inquest eval -cases cases.json
```

| outcome | meaning |
| --- | --- |
| `correct` | the responsible service ranked first |
| `in top 3` | second or third — a list a human can scan |
| `wrong` | inquest confidently named a service that was not responsible |
| `declined` | nothing cleared the threshold |

`wrong` and `declined` are counted separately because a localizer that stays
quiet when unsure is usable at 3am and one that is confidently wrong is not,
and a single hit rate hides which one you built. `inquest eval` exits non-zero
if any case is `wrong`.

A service that only appears as *waiting on something below it* scores as a
miss, never a hit. Counting it would let inquest mark its own homework on the
one distinction it claims to make.

## 6. Publish the number, including a bad one

Put it in the top-level README with the demo commit, the flags used, the window
length, and the case count. The README currently says any claim about
localization quality is unsupported, and that stays true until this is done.

## Optional: the same traces in Honeycomb

Uncomment the `otlp/honeycomb` exporter in `otelcol-config-extras.yml` and set
`HONEYCOMB_API_KEY` in the demo's `.env`, remembering to add it to the traces
exporter list rather than replacing what is there. Both backends then see
identical traces, so the incident inquest localizes can be opened in BubbleUp
beside it.

Honeycomb's Query Data API and MCP server are Enterprise-only, which is why
inquest reads the collector's file output instead of querying a backend.
Sending data works on the free tier; reading it back programmatically does not.


## Reproducing the published number

The three files that produced the figures in the root README's Accuracy section:

| file | what it does |
| --- | --- |
| `run-cases.sh` | drives one capture per flag, all flags off in between, and **verifies each injection after its window**, writing the evidence to the log |
| `cases.published.json` | the exact manifest that was scored, notes included |
| `score-cases.sh` | scores it under both ranking modes and both exclusion settings |

Run them from a checkout of the OpenTelemetry Demo with the collector extras in
place. `WIN` sets the window length in seconds, default 300.

Two things these encode that are easy to get wrong:

**Verify the injection, don't assume it.** A flag that is set is not a flag that
fired. `productCatalogFailure` ships with a targeting rule whose branches are
both `"off"`, and a targeting rule overrides `defaultVariant` — so setting the
default leaves it disabled while every log line says the config reloaded. It
scored as a clean decline for two runs. `setflag` now rewrites the matched
branch, and every case records error-span counts and service log evidence next
to its result.

**One baseline is not enough.** These scripts capture a single baseline and then
every incident window after it, which is the confound documented in the root
README: the ad service ended the run about 2.5x slower than it began with
nothing injected into it, and that drift is attributed to whichever flag was on.
Interleaving a fresh baseline between injections is the fix and has not been run
yet. Until it is, any number these produce is a floor on the error rate.
