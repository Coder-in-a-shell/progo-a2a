# A2A protocol scope

ProGoA2A uses A2A concepts—agent discovery, tasks, capabilities, artifacts, and streaming—but its current `/a2a/v1` API is a project-specific REST/JSON surface.

!!! warning "Not full A2A conformance"
    The implementation does not currently expose the official A2A JSON-RPC methods, Agent Card discovery document, protocol message/part types, push notifications, or protocol conformance metadata. Do not advertise it as a drop-in A2A server without adding and testing those capabilities.

## Routes

| Method | Path | Behavior |
|---|---|---|
| `GET` | `/a2a/v1/agents` | List configured agents |
| `GET` | `/a2a/v1/agents/{id}` | Get one agent card |
| `POST` | `/a2a/v1/tasks` | Synchronous task dispatch |
| `POST` | `/a2a/v1/tasks/stream` | Streaming task dispatch over SSE |
| `GET` | `/a2a/v1/tasks/{task_id}` | Retrieve a cached synchronous result |

See [A2A endpoint reference](../api/a2a-endpoints.md) for complete examples.

## Task request

```json
{
  "id": "optional-client-id",
  "agent_id": "researcher",
  "required_capability": "research",
  "input": "Summarize this paper",
  "context": {"thread_id": "thread-42"},
  "metadata": {"tenant": "example"},
  "stream": false
}
```

| Field | Type | Notes |
|---|---|---|
| `id` | string | Optional. The proxy generates `task-<16 hex chars>` when omitted. |
| `agent_id` | string | Direct target. Takes precedence if both routing fields are sent. |
| `required_capability` | string | Selects the first configured agent with a case-insensitive matching capability. |
| `input` | any JSON value | Passed to the selected adapter. |
| `context` | object | Adapter-specific context, such as LangGraph `thread_id` or OpenAI `system`. |
| `metadata` | object | Preserved in the internal task; current built-in adapters do not forward it. |
| `stream` | boolean | The HTTP streaming route sets this internally. |

At least one of `agent_id` or `required_capability` must be present.

## Task response

```json
{
  "task_id": "task-0123456789abcdef",
  "agent_id": "researcher",
  "status": "COMPLETED",
  "output": "Result text or structured JSON",
  "artifacts": [],
  "timestamp": "2026-09-10T00:00:00Z"
}
```

Status values defined by the model are `PENDING`, `IN_PROGRESS`, `COMPLETED`, `FAILED`, and `CANCELED`. Built-in synchronous adapters normally return `COMPLETED` or `FAILED`; a custom adapter may map additional statuses.

## Result retrieval

Synchronous results are cached in the process and can be read with `GET /a2a/v1/tasks/{task_id}`. This is retrieval after a completed synchronous request, not an asynchronous job queue:

- task submission blocks until the upstream responds;
- streaming results are not inserted in the cache;
- the cache holds at most 10,000 entries and evicts the oldest;
- entries disappear on restart and are not shared across replicas.

Use an external durable store if clients need reliable cross-replica retrieval.
