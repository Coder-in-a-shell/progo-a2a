# Direct REST endpoints

The `/api/v1` surface is the simplest way to invoke a known agent ID. It uses the same dispatcher, adapters, authorization, result schema, retries, fallbacks, and task storage (in-memory FIFO cache or durable PostgreSQL) as the A2A-inspired synchronous route.

## `POST /api/v1/invoke/{agent_id}`

```bash
curl -sS http://localhost:8080/api/v1/invoke/openai-chat \
  -H "Authorization: Bearer $PROXY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"input":"Explain exponential backoff","context":{"system":"Be concise"}}'
```

When the body is an object containing `input`, the handler builds a task from `input` and also recognizes object-valued `context` and `metadata`.

```json
{
  "input": "Explain exponential backoff",
  "context": {"system": "Be concise"},
  "metadata": {"tenant": "example"}
}
```

If a JSON object has no `input` key, the entire object becomes the task input. Arrays, strings, numbers, booleans, and `null` also become the input directly. An empty body results in a nil input.

The synchronous response is the same `TaskResponse` documented for [`POST /a2a/v1/tasks`](a2a-endpoints.md#post-a2av1tasks).

## `POST /api/v1/stream/{agent_id}`

```bash
curl -N -sS http://localhost:8080/api/v1/stream/openai-chat \
  -H "Authorization: Bearer $PROXY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"input":"Write one sentence"}'
```

Body interpretation matches the synchronous direct route. The response uses the same normalized event format as [`POST /a2a/v1/tasks/stream`](a2a-endpoints.md#post-a2av1tasksstream).

## Choosing a surface

| Need | Use |
|---|---|
| Invoke a known agent with minimal ceremony | Direct REST |
| Select by capability | A2A-inspired task route |
| Discover allowed agents | A2A-inspired agent routes |
| Retrieve an already completed stored result | A2A-inspired task GET |

Neither surface currently implements asynchronous job submission. The A2A-inspired label describes project concepts and naming, not full official-protocol conformance.
