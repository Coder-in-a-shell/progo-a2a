# Custom webhooks adapter

The `custom` adapter supports HTTP services whose request body and response fields do not match a built-in adapter. It combines a Go text template for requests with GJSON-style extraction for responses and SSE data.

## Complete shape

```yaml
agents:
  - id: knowledge-base
    name: Knowledge base
    type: custom
    endpoint: https://agent.example.com/v1/execute
    timeout_seconds: 30
    retries: 2
    auth:
      type: header
      header_name: X-Agent-Token
      header_value: "${AGENT_TOKEN}"
    mapping:
      request:
        method: POST
        headers:
          Content-Type: application/json
        body_template: |
          {
            "task_id": {{ .Task.ID | toJson }},
            "query": {{ .Task.Input | toJson }},
            "session": {{ default "main" .Task.Context.session_id | toJson }}
          }
      response:
        output_path: $.result.answer
        status_path: $.result.status
        artifacts_path: $.result.artifacts
        error_path: $.error.message
      stream:
        data_path: $.delta.text
        done_sentinel: "[DONE]"
```

A `mapping` block is required when `type` is `custom`.

## Request mapping

`method` defaults to `POST`. Headers are static strings; use the separate `auth` block for a bearer or secret header. `body_template` is optional and executes with:

| Value | Contents |
|---|---|
| `.Task` | `ID`, `AgentID`, `RequiredCapability`, `Input`, `Context`, `Metadata`, `Stream` |
| `.Agent` | The full configured agent entry |

Template helpers:

```gotemplate
{{ .Task.Input | toJson }}
{{ default "main" .Task.Context.session_id | toJson }}
{{ env "TENANT_ID" | toJson }}
```

Always use `toJson` for values placed inside JSON. Direct interpolation may produce invalid JSON or allow untrusted text to alter the payload.

For streaming calls the adapter adds `Accept: text/event-stream`, even if the static headers specify a different value.

## Response mapping

The fields are optional:

| Field | Behavior |
|---|---|
| `output_path` | Extracts `output`; with no path, output is the raw response string |
| `status_path` | Normalizes `error/failed`, `canceled/cancelled`, `in_progress/running`, `pending`, and `success/completed/done` |
| `artifacts_path` | Reads an array of objects with `name`, `mime_type`, `uri`, and `data` |
| `error_path` | Extracts an error for HTTP errors or a mapped `FAILED` status |

Paths accept GJSON dot notation plus normalized forms such as `$.result.answer`, `$['result']['answer']`, and `$.items[0].text`. This is not the full JSONPath language.

If an extraction path is absent in a successful response, its corresponding output remains empty. An unknown status value leaves the default status as `COMPLETED`.

## Streaming mapping

The custom adapter reads lines beginning with `data:`. If the trimmed data equals `done_sentinel`, it finishes. Otherwise it applies `data_path` to the JSON text and emits the extracted value as `token_delta`; if the path is absent or does not match, it emits the entire data string.

```text
data: {"delta":{"text":"Hello"}}
data: [DONE]
```

becomes:

```text
event: token_delta
data: {"delta":"Hello"}
```

The adapter does not interpret upstream `event:` names or JSON error fields. An HTTP status of 400 or greater emits `task_error`; mid-stream application errors need to be represented and handled by your client or by a purpose-built adapter.

## GET and bodyless calls

Any HTTP method string can be configured. For a bodyless request, omit `body_template`:

```yaml
mapping:
  request:
    method: GET
    headers:
      Accept: application/json
  response:
    output_path: $.result
```

The adapter does not template the URL or query string. Use a fixed endpoint or extend the adapter for dynamic query parameters.
