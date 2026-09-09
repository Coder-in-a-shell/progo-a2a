# Real-time streaming

Streaming routes return Server-Sent Events (SSE) using `Content-Type: text/event-stream`:

- `POST /a2a/v1/tasks/stream`
- `POST /api/v1/stream/{agent_id}`

## Response headers

The SSE writer sets:

```http
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
X-Accel-Buffering: no
```

It flushes headers immediately and flushes after every event. A mutex serializes writes to a response writer.

## Wire format

Each event is written as:

```text
event: <type>
data: <JSON value>

```

The `data` value is the event payload itself; it is not wrapped in the model's `StreamEvent` struct.

## Event types

| Type | Typical data | Meaning |
|---|---|---|
| `task_started` | `{"agent_id":"agent-a"}` | Adapter accepted the upstream response and began processing it |
| `step_progress` | `{"step":"updates","data":{...}}` | Framework-specific structured progress |
| `token_delta` | `{"delta":"text"}` | Incremental output |
| `task_completed` | `{"status":"COMPLETED"}` | Upstream stream ended normally |
| `task_error` | `{"error":"...","agent_id":"agent-a"}` | Upstream or dispatch error |

There is no `task_failed` event in the current implementation.

## Client example

Browser `EventSource` only issues GET requests, so use `fetch()` for these POST routes:

```javascript
const response = await fetch("http://localhost:8080/a2a/v1/tasks/stream", {
  method: "POST",
  headers: {
    "Authorization": "Bearer local-client-key",
    "Content-Type": "application/json",
  },
  body: JSON.stringify({agent_id: "openai-chat", input: "Write one sentence."}),
});

if (!response.ok || !response.body) {
  throw new Error(`HTTP ${response.status}`);
}

const reader = response.body.getReader();
const decoder = new TextDecoder();
let buffer = "";

while (true) {
  const {value, done} = await reader.read();
  if (done) break;
  buffer += decoder.decode(value, {stream: true});

  let boundary;
  while ((boundary = buffer.indexOf("\n\n")) !== -1) {
    const frame = buffer.slice(0, boundary);
    buffer = buffer.slice(boundary + 2);
    console.log(frame);
  }
}
```

For a terminal:

```bash
curl -N -sS http://localhost:8080/api/v1/stream/openai-chat \
  -H 'Authorization: Bearer local-client-key' \
  -H 'Content-Type: application/json' \
  -d '{"input":"Write one sentence."}'
```

## Upstream assumptions

All built-in streaming adapters read line-oriented responses with a scanner limit of 10 MiB per line. They primarily process lines beginning with `data:`. The exact fields recognized differ by adapter; see the individual adapter guides.

The service does not currently emit heartbeats, SSE `id` fields, reconnection hints, or resumable offsets. Configure intermediary idle timeouts accordingly.

## Failure behavior

The dispatcher can retry or fall back before an adapter emits its first event. Once any event—including `task_started`—has been delivered, it will not switch upstreams. A subsequent error is sent as `task_error` on the same stream.
