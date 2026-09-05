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

Pre-alpha. Nothing works yet.

## Accuracy

The point of this project is a published, reproducible accuracy number against
labeled failures. Until that number exists here, treat any claim about
localization quality as unsupported.

## License

MIT
