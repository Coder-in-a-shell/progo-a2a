# Translation engine

Each adapter implements three operations:

```go
type Adapter interface {
    Type() string
    TranslateRequest(context.Context, *config.AgentConfig, *model.TaskRequest) (*http.Request, error)
    TranslateResponse(context.Context, *config.AgentConfig, *http.Response) (*model.TaskResponse, error)
    TranslateStream(context.Context, *config.AgentConfig, *http.Response, stream.Emitter) error
}
```

Five adapters are registered at startup under exact, case-sensitive type names: `langgraph`, `crewai`, `autogen`, `openai`, and `custom`.

## Common behavior

- Every adapter sends to `agents[].endpoint`.
- `auth.type: bearer` adds `Authorization: Bearer <token>`.
- `auth.type: header` adds the configured header name/value.
- Streaming requests add `Accept: text/event-stream`.
- Synchronous adapter responses set `agent_id`, a UTC `timestamp`, and a normalized status.
- The dispatcher supplies a task ID when an adapter does not.

## Built-in request shapes

### LangGraph

```json
{
  "input": "task input",
  "config": {"configurable": {"thread_id": "task-or-context-id"}},
  "assistant_id": "optional",
  "stream_mode": "optional"
}
```

`options.config` is merged into `config`. A request `context.thread_id` overrides `options.config.configurable.thread_id`. Responses prefer `output`, then `values`, then the whole parsed body.

### CrewAI

```json
{"inputs":{"input":"task input"}}
```

If the task input is already an object, that object becomes `inputs`. Other `options` become top-level payload fields. Responses prefer `result`, `raw`, `output`, then `tasks_output`.

### AutoGen

```json
{"sender":"user","recipient":"configured-agent","message":"task input"}
```

Sender and recipient can come from options and can be overridden by request context. Responses prefer `reply`, `response`, `summary`, `message`, then the final `chat_history[].content`.

### OpenAI

```json
{
  "model":"gpt-4o",
  "messages":[{"role":"user","content":"task input"}],
  "stream":false
}
```

The default model is `gpt-4o`. `options.system_prompt` or `context.system` prepends a system message. Other options are forwarded at the top level. Responses prefer `choices[0].message.content`, tool calls, then legacy `choices[0].text`.

### Custom

The custom adapter executes a Go `text/template` body with `.Task` and `.Agent`. Available helpers are:

| Function | Purpose |
|---|---|
| `toJson` | JSON-encode a value for safe insertion |
| `default` | Return a fallback for nil or empty-string values |
| `env` | Read an environment variable at request time |

```yaml
mapping:
  request:
    method: POST
    headers:
      Content-Type: application/json
    body_template: '{"query": {{ .Task.Input | toJson }}}'
  response:
    output_path: $.result.answer
    status_path: $.result.status
    artifacts_path: $.result.artifacts
    error_path: $.error.message
  stream:
    data_path: $.delta.text
    done_sentinel: "[DONE]"
```

Extraction uses GJSON after normalizing common forms such as `$.items[0].text`. It is not a complete JSONPath implementation.

## Status normalization

Built-in adapters mark HTTP 4xx/5xx responses as `FAILED`, although the dispatcher intercepts and retries 5xx responses before translation. The custom adapter additionally maps configured response status strings:

| Upstream value | Status |
|---|---|
| `error`, `failed` | `FAILED` |
| `canceled`, `cancelled` | `CANCELED` |
| `in_progress`, `running` | `IN_PROGRESS` |
| `pending` | `PENDING` |
| `success`, `completed`, `done` | `COMPLETED` |

Unknown values leave the default `COMPLETED` status unchanged.
