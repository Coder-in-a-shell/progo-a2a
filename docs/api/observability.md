# Health, metrics, and logs

## Health endpoints

### `GET /healthz`

Always returns `200` while the process can serve HTTP:

```json
{"status":"healthy","time":"2026-09-10T00:00:00Z"}
```

It does not validate configuration or upstream connectivity.

### `GET /readyz`

Returns `200` when a configuration pointer exists and the adapter registry contains at least one adapter:

```json
{"status":"ready","adapters":5,"time":"2026-09-10T00:00:00Z"}
```

Otherwise it returns `503` with `status: "not ready"`. It does not probe configured upstream endpoints or credentials.

Both health endpoints, plus `/metrics`, bypass API-key authentication.

## Operator console

`GET /console/` serves a responsive, read-only operator console directly from the Go binary. It visualizes liveness, readiness, configured agents, request volume, average observed latency, and active streams using the existing APIs and Prometheus exposition.

The console shell is public so it can load before authentication. Its bootstrap endpoint exposes only whether authentication is required, the runtime role, the storage backend name, and the refresh interval. It never returns credentials, agent configuration secrets, or upstream endpoints. When API-key security is enabled, agent inventory remains protected; an operator can connect a key that is kept in `sessionStorage` for the current browser tab and sent only to same-origin gateway APIs.

The console does not currently submit or cancel durable jobs, modify configuration, or administer tenants. Those actions require a separately authorized management API.

## Prometheus exposition

`GET /metrics` returns Prometheus text format from an in-process collector.

| Metric | Type | Labels |
|---|---|---|
| `a2a_requests_total` | counter | `method`, `path`, `status` |
| `a2a_request_duration_seconds` | summary-style sum/count | `method`, `path` |
| `a2a_active_streams` | gauge | none |
| `a2a_retries_total` | counter | `agent_id` when labeled data exists |
| `a2a_fallback_triggered_total` | counter | `primary`, `fallback` when labeled data exists |

The duration collector exposes `_sum` and `_count`; it does not expose histogram buckets or quantiles. Average latency over a window can be calculated with:

```promql
sum(rate(a2a_request_duration_seconds_sum[5m]))
/
sum(rate(a2a_request_duration_seconds_count[5m]))
```

Other useful queries:

```promql
sum by (path) (rate(a2a_requests_total[5m]))
```

```promql
sum(rate(a2a_requests_total{status=~"5.."}[5m]))
```

```promql
a2a_active_streams
```

Example scrape configuration:

```yaml
scrape_configs:
  - job_name: progo-a2a
    metrics_path: /metrics
    static_configs:
      - targets: ["progo-a2a:8080"]
```

Metrics reset when the process restarts and are not aggregated across replicas; Prometheus handles cross-replica aggregation after scraping each instance.

## Structured logs

The service uses Go `slog` with a JSON handler writing to stdout. Set the minimum level with `-log-level debug|info|warn|error`.

An HTTP completion log has these fields:

```json
{
  "time": "2026-09-10T00:00:00Z",
  "level": "INFO",
  "msg": "http request",
  "trace_id": "550e8400-e29b-41d4-a716-446655440000",
  "method": "POST",
  "path": "/a2a/v1/tasks",
  "status": 200,
  "duration_ms": 342
}
```

The request ID is also returned as `X-Request-ID`. A client-provided header is preserved without format validation; otherwise the middleware generates a UUID v4 (with a timestamp fallback if secure random generation fails).

Retry logs use `msg: "retrying agent invocation"` or `"retrying streaming agent invocation"` and include `agent_id`, `attempt`, `max_retries`, and `error`. Fallback logs use `msg: "failing over to fallback agent"` (or its streaming variant) with `from_agent_id` and `to_agent_id`.

The current HTTP completion log does not include `client_id` or `agent_id`, and debug mode does not log upstream payloads. Avoid designing audit or troubleshooting procedures around fields that are not emitted.

## Kubernetes probes

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080
readinessProbe:
  httpGet:
    path: /readyz
    port: 8080
```

See [Kubernetes deployment](../deployment/kubernetes.md) for a complete example.
