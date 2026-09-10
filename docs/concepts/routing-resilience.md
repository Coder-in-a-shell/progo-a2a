# Routing and resilience

## Agent selection

Direct routing uses `agent_id`. Capability routing scans agents in configuration order and returns the first case-insensitive capability match.

```json
{"agent_id":"primary","input":"hello"}
```

```json
{"required_capability":"chat","input":"hello"}
```

If both fields are present, `agent_id` wins. If neither is present, the proxy returns `400 INVALID_REQUEST`.

## Attempts and retries

`retries` is the number of additional attempts after the initial call. For `retries: 2`, the selected agent may be called three times.

Retries use exponential backoff beginning at 100 ms, multiplying by 2, capped at 2 seconds, with a random 0.8–1.2 factor. A fresh `timeout_seconds` context is created for every attempt.

The dispatcher retries transport failures and HTTP 5xx responses. An HTTP 4xx response is translated into a `FAILED` task response by the adapter and is not retried. Request/response translation failures stop retries for the current agent and may lead to a configured fallback.

## Fallbacks

After the selected agent's retry budget is exhausted, `fallback_agent_ids` are tried in order. Each fallback has its own timeout, retry count, adapter, and further fallback list.

```yaml
agents:
  - id: primary
    type: openai
    endpoint: https://example.invalid/v1/chat/completions
    retries: 2
    fallback_agent_ids: [secondary]
    options:
      model: example-model

  - id: secondary
    type: custom
    endpoint: https://backup.example.invalid/invoke
    retries: 0
    mapping:
      request:
        method: POST
        headers: {Content-Type: application/json}
        body_template: '{"input": {{ .Task.Input | toJson }}}'
      response:
        output_path: $.output
```

Configuration validation rejects missing fallback IDs, self-fallbacks, and cycles before the server starts. The dispatcher also tracks visited agents defensively at runtime.

## Streaming boundary

Streaming fallback is possible only before any downstream event is emitted to the client. In practice, each adapter emits `task_started` as soon as it accepts a successful upstream streaming response, so transport failures and upstream 5xx responses can fall back, while errors encountered after stream processing starts cannot. This prevents mixing two upstream streams in one client response.

## Cancellation

Client cancellation and parent-context deadlines stop backoff and prevent further fallback attempts. Adapter scanners also check the request context while reading upstream streams.

## Connection pooling

The shared transport is cloned from Go's default transport and configured with:

- 100 maximum idle connections;
- 100 maximum idle connections per host;
- 90-second idle connection timeout.

There is no configurable circuit breaker, rate limiter, bulkhead, or maximum-concurrency policy in the current implementation. Add those at the edge or in the service before relying on them for production isolation.

## Task storage

Successful synchronous dispatches, including translated `FAILED` task responses, are stored using the configured storage backend:

- **In-memory (`backend: memory`)**: A thread-safe, bounded FIFO cache (default 10,000 entries, configurable up to 1,000,000 entries via `storage.memory.max_tasks`). It operates process-locally with no persistence across restarts or sharing across replicas.
- **PostgreSQL (`backend: postgres`)**: A durable, shared PostgreSQL store (CI-tested with PostgreSQL 17). Lookup keys (canonical task ID and any alias IDs) are upserted atomically in a single statement, preserving `created_at` timestamps on conflict. Completed results can be queried by any proxy replica connecting to the same database.

### Storage failure semantics

When using durable storage, if an upstream agent responds successfully but saving to PostgreSQL fails (for example, due to a connection error or query timeout), the proxy logs the sanitized error and responds to the client with HTTP `503 Service Unavailable` (`TASK_STORAGE_UNAVAILABLE`). This guarantees that callers are notified if a task was not durably recorded. The downstream operation may nevertheless have completed, so callers must not blindly retry non-idempotent work.

Be precise about boundaries: PostgreSQL makes completed synchronous task lookup durable and shared across replicas; it does not create asynchronous background jobs, persist streaming events, add cancellation, provide automatic retention, or implement official A2A 1.0. See the [PostgreSQL storage guide](../deployment/postgresql.md) for full configuration and tuning options.
