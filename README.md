# inquest

Deterministic root-cause localization for distributed systems.

Given an incident window and a symptom, inquest identifies the service and
deploy most likely responsible — with the evidence and the scoring that
produced the ranking, or an explicit "no code change explains this."

## Design

The causal engine is deterministic. It walks the OpenTelemetry trace DAG to
find the **deepest span whose deviation from baseline is not explained by its
children**, then follows resource attributes through build provenance to a
commit range and a diff.

An LLM is optional and is used only to narrate a ranking it did not produce.

Two properties follow, and neither is available to an LLM-first design:

- **Reproducible** — the same incident yields the same answer.
- **Auditable** — the inputs and the scoring are shown.

## Status

Pre-alpha. The localizer works and has been run once against a real incident;
there is no accuracy number yet.

`inquest localize` reads two windows of OTLP/JSON from the OpenTelemetry
Collector's file exporter, ranks operations, and writes a self-contained HTML
report. `inquest eval` scores a manifest of labeled incidents. `deploy/` has
the runbook for producing those windows from the OpenTelemetry Demo.

## Accuracy

The point of this project is a published, reproducible accuracy number against
labeled failures. **That number does not exist yet, so treat any claim about
localization quality as unsupported.**

What has been run, once, on opentelemetry-demo `8c47d47`:

| flag | expected | inquest said | |
| --- | --- | --- | --- |
| `adManualGc` | `ad` | `ad · oteldemo.AdService/GetAds` — self time 6.963ms → 1893.08ms, z 409.99 | correct |

The runner-up scored 0.52, so the margin was not close. One case is an anecdote.
`deploy/README.md` is how the rest get produced.

**Caveat on that row.** The capture files behind it were deleted, and two later
attempts to rerun the window produced no injection at all — the demo's
`adManualGc` flag logged zero collections in the ad service both times. The full
ranking survives in [`evidence/adManualGc-ranking.json`](evidence/adManualGc-ranking.json),
recovered from the generated report, so the figures are checkable; they are not
reproducible from raw spans. Treat it as a record of a measurement, not as a
result that has been confirmed twice.

### What that one case already taught

The frontend's client span for the same call moved **+1885.6ms in duration and
+3.6ms in self time** — it was waiting, not slow. inquest called it *slower*,
because the classifier tested an absolute floor before the proportion and 3.6ms
clears any sane floor. Real traces are noisy in a way the synthetic tests were
not. Waiting is now decided on proportion first, and the demo's own numbers are
a regression test.

`intlShippingSlowdown` looked like the cleanest case on paper and is not usable:
it only delays non-US addresses, and one of the nine load-generator personas is
Canadian. Roughly a tenth of an already-rare operation is affected, which is
below the sample floor and a tail effect rather than a shift.

## License

MIT
