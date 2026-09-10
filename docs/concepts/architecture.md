# Architecture

ProGoA2A is one Go HTTP process. Configuration is loaded at startup, five adapters are registered, and a shared dispatcher sends translated requests through a pooled `http.Client`.

## Request lifecycle

```mermaid
sequenceDiagram
    participant C as Client
    participant M as Middleware
    participant H as HTTP handler
    participant D as Dispatcher
    participant A as Adapter
    participant U as Upstream

    C->>M: HTTP request
    M->>M: recover, request ID, metrics, log, auth
    M->>H: authorized request
    H->>D: TaskRequest
    D->>D: resolve agent
    D->>A: translate request
    A->>U: upstream HTTP request
    U-->>A: response or SSE
    A-->>D: TaskResponse or normalized events
    D-->>H: result
    H-->>C: JSON or SSE
```

## Package map

| Path | Responsibility |
|---|---|
| `cmd/proxy` | CLI flags, config loading, adapter registration, server lifecycle, signal handling |
| `pkg/config` | YAML model, `${ENV_VAR}` expansion, defaults, validation, fallback-cycle detection |
| `pkg/model` | Agent, task, artifact, event, and error types |
| `pkg/adapter` | Built-in translations and adapter registry |
| `pkg/dispatcher` | Agent selection, retry/backoff, timeout, fallback, pooled HTTP transport |
| `pkg/server` | Routes, handlers, middleware, task storage integration, health/readiness |
| `pkg/storage` | TaskStore interface, in-memory FIFO cache, and PostgreSQL durable backend with migrations |
| `pkg/stream` | Thread-safe SSE writer |
| `pkg/metrics` | In-process Prometheus text collector |
| `tests` | End-to-end and benchmark suites with local mock agents |

## Middleware order

Requests pass through:

1. panic recovery;
2. request-ID creation or propagation;
3. request metrics;
4. structured request logging;
5. API-key authentication;
6. the matched handler.

The response includes `X-Request-ID`. Logs use the field `trace_id` and include method, path, status, and integer duration in milliseconds.

## Server lifecycle

The server uses configured read, write, and idle timeouts. SSE handlers clear the per-response write deadline so a long stream is not cut off by `write_timeout_seconds`. On `SIGINT` or `SIGTERM`, the process requests graceful shutdown with a fixed 15-second deadline.

## State and scaling

Routing/configuration is read-only after startup. The only request-derived application state is completed synchronous task storage and in-process metrics.

When configured with the default `memory` backend, task results are stored in a process-local, bounded FIFO cache (default 10,000 entries) that resets on restart and is not shared across replicas. When configured with the `postgres` backend, completed synchronous task results are persisted to a shared PostgreSQL database, enabling durable task lookup across horizontal replicas and process restarts. Metrics remain in-process and process-local.

Be precise about storage boundaries: PostgreSQL makes completed synchronous task lookup durable and shared across replicas; it does not create asynchronous background jobs, persist streaming events, add cancellation, provide automatic retention, or implement official A2A 1.0. Horizontal replicas work seamlessly for stateless invocation and durable task retrieval, while Prometheus metrics should be scraped per replica. See the [PostgreSQL storage guide](../deployment/postgresql.md) for full deployment and operational details.
