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

## Task-result cache

Successful synchronous dispatches, including translated `FAILED` task responses, are kept in a thread-safe 10,000-entry FIFO cache. It has no TTL, persistence, cross-replica sharing, or public configuration field.
