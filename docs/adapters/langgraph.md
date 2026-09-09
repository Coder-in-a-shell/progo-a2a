# LangGraph adapter

Use `type: langgraph` for an HTTP endpoint that accepts the payload below and, when streaming, returns line-oriented SSE.

## Request translation

Given a task input of `{"messages":[...]}`, the adapter sends:

```json
{
  "input": {"messages": ["..."]},
  "config": {
    "configurable": {"thread_id": "task-0123456789abcdef"}
  },
  "assistant_id": "researcher",
  "stream_mode": "messages"
}
```

The thread ID is the task `id`, unless `context.thread_id` is a non-empty string. The supported options are:

| Option | Behavior |
|---|---|
| `assistant_id` | Copied to the top-level request |
| `config` | Merged into the request `config` object |
| `stream_mode` | Copied to the top-level request |

Within `options.config.configurable`, the request-derived non-empty thread ID wins over an option with the same name.

```yaml
agents:
  - id: langgraph-researcher
    name: LangGraph researcher
    type: langgraph
    endpoint: http://localhost:8000/runs/wait
    capabilities: [research]
    timeout_seconds: 60
    retries: 2
    auth:
      type: bearer
      token: "${LANGGRAPH_API_KEY}"
    options:
      assistant_id: researcher
      stream_mode: messages
      config:
        configurable:
          tenant: example
```

The adapter uses exactly the configured endpoint for both synchronous and streaming calls; it does not rewrite `/runs/wait` to `/runs/stream`. Configure an endpoint that supports the mode you call, or create separate agent entries.

## Synchronous response

For HTTP responses below 400, output extraction uses this order:

1. top-level `output`;
2. top-level `values`;
3. the entire parsed JSON body, or raw text if it is not JSON.

The adapter does not unwrap `output.output` or inspect a success-status field.

For HTTP 4xx, it returns a `FAILED` task and chooses the error text from `detail`, `error`, `message`, or the raw response. The dispatcher intercepts HTTP 5xx for retry/fallback before the adapter receives it.

## Streaming response

The adapter recognizes SSE `event:` and `data:` lines:

| Upstream event | Normalized output |
|---|---|
| `messages`, `message`, `token` | `token_delta`; prefers array item `0.content`, then `content`, then parsed data |
| `values`, `updates`, `steps`, `step` | `step_progress` |
| `error` | `task_error`, then stream termination |
| `end` or `data: [DONE]` | end-of-stream |
| other | `token_delta` when JSON has `delta`, otherwise `step_progress` |

Normal completion emits `task_completed`.

## Compatibility checklist

- Confirm your endpoint accepts the exact payload shape.
- Confirm the same configured endpoint supports the requested sync/stream mode.
- For stateful runs, send an explicit task `id` or `context.thread_id`.
- Inspect raw upstream SSE event names when data appears as `step_progress` rather than tokens.
