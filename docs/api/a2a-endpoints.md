# A2A-inspired endpoints

These routes use A2A concepts but are not a complete implementation of the official A2A JSON-RPC protocol. See [protocol scope](../concepts/a2a-protocol.md).

## Authentication

When `security.enabled` is true, send either:

```http
Authorization: Bearer <proxy-api-key>
```

or:

```http
X-API-Key: <proxy-api-key>
```

Keys are configured under `security.api_keys`. Discovery is filtered to the key's agent allowlist, direct agent lookup/dispatch is authorized against that allowlist, and cached task retrieval re-checks the stored result's agent.

## `GET /a2a/v1/agents`

Lists configured agents visible to the caller.

```json
{
  "agents": [
    {
      "id": "openai-chat",
      "name": "OpenAI chat",
      "description": "General chat upstream",
      "capabilities": ["chat"],
      "tags": ["openai"]
    }
  ]
}
```

Adapter type and endpoint are intentionally not part of the agent card.

## `GET /a2a/v1/agents/{id}`

Returns one agent card or `404 AGENT_NOT_FOUND`. With security enabled, requesting an agent outside the key's allowlist returns `403`.

```bash
curl -sS http://localhost:8080/a2a/v1/agents/openai-chat \
  -H "Authorization: Bearer $PROXY_API_KEY"
```

## `POST /a2a/v1/tasks`

Synchronously invokes an agent. The body must be JSON and no larger than 10 MiB.

Direct routing:

```json
{
  "id": "optional-client-task-id",
  "agent_id": "openai-chat",
  "input": "Summarize retries",
  "context": {"system": "Answer briefly"},
  "metadata": {"tenant": "example"}
}
```

Capability routing:

```json
{"required_capability":"chat","input":"Hello"}
```

`agent_id` takes precedence if both routing fields are supplied. Capability matching is case-insensitive and chooses the first configured match; authorization is checked after selection.

Success response:

```json
{
  "task_id": "task-0123456789abcdef",
  "agent_id": "openai-chat",
  "status": "COMPLETED",
  "output": "Retries repeat a failed upstream call according to policy.",
  "timestamp": "2026-09-10T00:00:00Z"
}
```

`output` may be any JSON value. `artifacts` and `error` are omitted when empty because they use `omitempty`.

```bash
curl -sS http://localhost:8080/a2a/v1/tasks \
  -H "Authorization: Bearer $PROXY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"agent_id":"openai-chat","input":"Hello"}'
```

## `POST /a2a/v1/tasks/stream`

Uses the same request schema and returns normalized SSE. Depending on the adapter, events can be `task_started`, `step_progress`, `token_delta`, `task_completed`, or `task_error`.

```text
event: task_started
data: {"agent_id":"openai-chat"}

event: token_delta
data: {"delta":"Hello"}

event: task_completed
data: {"status":"COMPLETED"}
```

```bash
curl -N -sS http://localhost:8080/a2a/v1/tasks/stream \
  -H "Authorization: Bearer $PROXY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"agent_id":"openai-chat","input":"Hello"}'
```

See [streaming](../concepts/streaming.md) for failure and fallback boundaries.

## `GET /a2a/v1/tasks/{task_id}`

Returns a cached result from a completed synchronous A2A or REST invocation. It does not poll an active job.

```bash
curl -sS http://localhost:8080/a2a/v1/tasks/task-0123456789abcdef \
  -H "Authorization: Bearer $PROXY_API_KEY"
```

The cache is per-process, FIFO, capped at 10,000 results, and cleared on restart. Streaming results are not cached.

## Errors

Errors use this envelope:

```json
{
  "error": {
    "code": "AGENT_NOT_FOUND",
    "message": "agent 'missing' not found",
    "agent_id": "missing",
    "status": 404,
    "timestamp": "2026-09-10T00:00:00Z"
  }
}
```

Common statuses:

| HTTP | Typical code/cause |
|---|---|
| `400` | `INVALID_REQUEST`, malformed JSON or missing routing field |
| `401` | `UNAUTHORIZED`, missing/invalid proxy API key |
| `403` | `FORBIDDEN`, agent outside key allowlist |
| `404` | `AGENT_NOT_FOUND`, `CAPABILITY_NOT_FOUND`, or `TASK_NOT_FOUND` |
| `413` | `REQUEST_TOO_LARGE`, body exceeds 10 MiB |
| `500` | `INTERNAL_ERROR` or unsupported response streaming |
| `502` | `DOWNSTREAM_UNAVAILABLE` after attempts/fallbacks |
| `504` | `DOWNSTREAM_TIMEOUT` |

After SSE headers are sent, failures are emitted as `task_error` events instead of changing the HTTP status.
