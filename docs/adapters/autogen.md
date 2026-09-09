# AutoGen adapter

Use `type: autogen` for an HTTP wrapper around an AutoGen workflow that accepts `sender`, `recipient`, and `message`. AutoGen deployments do not share one universal REST contract, so treat this as a concrete compatibility shape.

## Request translation

```json
{
  "sender": "user",
  "recipient": "assistant-bot",
  "message": "Review this function"
}
```

Resolution rules:

- `sender` defaults to `user`, can be set in options, and can be overridden by `context.sender`.
- `recipient` defaults to the configured agent ID (or name when the ID is empty), can be set in options, and can be overridden by `context.recipient`.
- every other option except `message` is copied to the top-level request.
- request context is not otherwise forwarded.

```yaml
agents:
  - id: autogen-coder
    name: AutoGen coder
    type: autogen
    endpoint: http://localhost:8088/chat
    capabilities: [coding]
    timeout_seconds: 90
    retries: 1
    auth:
      type: bearer
      token: "${AUTOGEN_API_KEY}"
    options:
      sender: user-proxy
      recipient: assistant-bot
```

## Synchronous response

Output extraction order is:

1. `reply`;
2. `response`;
3. `summary`;
4. `message`;
5. content from the final `chat_history` item;
6. the full parsed body or raw text.

The adapter does not inspect a top-level status field on successful HTTP responses. HTTP 4xx becomes a `FAILED` task; HTTP 5xx is retried by the dispatcher.

## Streaming response

The adapter reads SSE `data:` lines:

- `error` emits `task_error` and stops;
- `step` emits `step_progress`;
- `delta`, `content`, or `message` emits `token_delta` using the whole value of that field;
- other JSON and plain text emits `token_delta`;
- `[DONE]` or EOF completes the stream.

If your upstream sends `{"delta":{"content":"text"}}`, the normalized `delta` remains an object. Use a custom adapter when a nested extraction path is required.
