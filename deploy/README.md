# Running inquest against the OpenTelemetry Demo

The point of this directory is an accuracy number produced from failures
inquest did not author. `inquest demo` proves the plumbing works and nothing
else — the generator picks the answer there, so finding it means nothing.

The [OpenTelemetry Demo](https://github.com/open-telemetry/opentelemetry-demo)
ships feature flags that inject specific failures. Flip one and you know the
right answer without inquest having any say in it. That is the entire reason
to use it.

## Before you start

The demo pulls roughly 10–15 GB of images across ~20 services. Check you have
the headroom:

```
df -h ~
docker system df
```

## 1. Bring up the demo with a file exporter

```
git clone https://github.com/open-telemetry/opentelemetry-demo.git
cd opentelemetry-demo
mkdir -p inquest-traces
cp /path/to/inquest/deploy/otelcol-config-extras.yml src/otel-collector/
docker compose -f docker-compose.yml -f /path/to/inquest/deploy/docker-compose.override.yml up -d
```

**Read `otelcol-config-extras.yml` before you copy it.** The demo merges that
file into the collector config, and the merge *replaces* lists rather than
appending. The `exporters:` line has to repeat the demo's own exporters or you
will silently turn everything else off. The current list is in
`src/otel-collector/otelcol-config.yml`, and it changes between releases.

Confirm spans are landing:

```
wc -l inquest-traces/traces.jsonl
```

## 2. Capture a baseline

Let the load generator run for a few minutes so the system is warm, then:

```
/path/to/inquest/deploy/capture.sh 180 baseline.jsonl
```

## 3. Inject one failure and capture the incident

The demo's flags live in `src/flagd/demo.flagd.json`. **Read that file rather
than trusting a list from anywhere else, this one included** — flag names have
changed between demo releases, and a manifest that names a flag the demo does
not have will produce a window where nothing happened and score as a decline.

Flip exactly one flag, wait for it to take effect, then:

```
/path/to/inquest/deploy/capture.sh 180 incident-<flag>.jsonl
```

Turn it back off before capturing the next one. Two flags at once produces a
window with two causes, and neither the manifest nor the scoring has a way to
express that.

## 4. Write down what you know

`cases.example.json` shows the shape. For each case, `expectService` is the
service whose *own work* changed — not the service where the symptom showed
up. Getting this wrong is the fastest way to publish a number that is worse
than the tool.

Where a flag's blast radius genuinely covers two services, say so in `notes`
and leave the case out of the scored set rather than picking the answer that
flatters the tool.

## 5. Score it

```
inquest eval -cases cases.json
```

Four outcomes, and they are not collapsed into one number:

| outcome | meaning |
| --- | --- |
| `correct` | the responsible service ranked first |
| `in top 3` | second or third — a list a human can scan |
| `wrong` | inquest confidently named a service that was not responsible |
| `declined` | nothing cleared the threshold |

`wrong` and `declined` are counted separately because a localizer that stays
quiet when it is unsure is usable at 3am and one that is confidently wrong is
not, and a single hit rate hides the difference. `inquest eval` exits non-zero
if any case is `wrong`.

## 6. Publish the number, including a bad one

Put it in the top-level README with the demo version, the flags used, the
window length, and the case count. A negative result described accurately is
worth more than a good one nobody can reproduce — and the README currently
says any claim about localization quality is unsupported, which stays true
until this is done.

## Optional: the same traces in Honeycomb

Uncomment the `otlp/honeycomb` exporter in `otelcol-config-extras.yml` and set
`HONEYCOMB_API_KEY` in the demo's `.env`. Both then see identical traces, so
the incident inquest localizes can be opened in BubbleUp beside it.

Note that Honeycomb's Query Data API and MCP server are Enterprise-only, which
is why inquest reads the collector's output instead of querying a backend.
Sending data works on the free tier; reading it back programmatically does not.
