# Production checklist

ProGoA2A has solid foundations, but the current repository is an early-stage gateway rather than a turnkey production platform. Complete the applicable items before exposing it to important traffic.

## Security

- [ ] Enable `security.enabled` and inject all keys through `${ENV_VAR}` references.
- [ ] Give each client a unique key and the smallest practical `allowed_agents` list.
- [ ] Terminate TLS at an ingress/reverse proxy; the service listens on HTTP.
- [ ] Add edge rate limiting, connection limits, and abuse controls.
- [ ] Restrict public access to `/metrics` if operational data is sensitive; it is public in the app.
- [ ] Preserve the image's non-root `progo-a2a` user and scan the image/dependencies.
- [ ] Validate required upstream environment variables before startup; unset adapter credentials become empty strings.

## Reliability

- [ ] Size `write_timeout_seconds` for total attempts, backoff, and fallbacks.
- [ ] Keep intermediary idle timeouts above expected SSE quiet periods; the app emits no heartbeats.
- [ ] Configure fallbacks only between agents with compatible task semantics.
- [ ] Account for the fixed 15-second application shutdown deadline.
- [ ] Add circuit breaking/concurrency controls externally or implement them; they are not built in.
- [ ] Load-test with realistic payloads and real upstream latency.

## State and scaling

- [ ] Decide whether cached task retrieval is required.
- [ ] If required across replicas/restarts, replace the in-memory 10,000-entry cache with shared durable storage.
- [ ] Scrape every replica; metrics are process-local.
- [ ] Remember that `/readyz` checks registration/config presence, not upstream health.

## Observability

- [ ] Scrape `/metrics` and create alerts for HTTP 5xx, retries, fallbacks, and active streams.
- [ ] Compute average duration from summary `_sum`/`_count`; histogram quantiles are unavailable.
- [ ] Aggregate JSON stdout logs and propagate `X-Request-ID` through callers.
- [ ] Add `client_id` and selected-agent fields to logs if per-client audit trails are required.
- [ ] Redact or avoid sensitive request/upstream payload logging in future changes.

## Delivery

- [ ] Require the CI checks: formatting, module verification, vet, race-enabled tests, build, and strict docs build.
- [ ] Publish immutable image tags/digests from a trusted pipeline.
- [ ] Use staged rollout and rollback procedures.
- [ ] Back up deployment config and document secret rotation.
- [ ] Review the [A2A protocol scope](../concepts/a2a-protocol.md) before claiming official compatibility.
