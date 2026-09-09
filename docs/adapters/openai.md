# OpenAI adapter

The `openai` adapter targets an OpenAI-compatible Chat Completions endpoint. It does not currently implement the Responses API.

## Configuration

```yaml
agents:
  - id: openai-chat
    name: OpenAI chat
    type: openai
    endpoint: https://api.openai.com/v1/chat/completions
    capabilities: [chat, summarization]
    timeout_seconds: 45
    retries: 2
    auth:
      type: bearer
      token: "${OPENAI_API_KEY}"
    options:
      model: gpt-4o
      temperature: 0.3
      max_tokens: 1024
      system_prompt: You are concise.
```

`model` defaults to `gpt-4o`. All options except `model`, `system_prompt`, `messages`, and `stream` are forwarded as top-level Chat Completions fields.

## Input conversion

String input becomes one user message:

```json
{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}]}
```

An array is used directly as `messages`. An object with a `messages` array uses that array; any other object becomes the content of one user message.

`context.system` overrides `options.system_prompt` and, when non-empty, prepends a system message. On the streaming route the adapter also sets `stream: true` and `Accept: text/event-stream`.

## Synchronous response

Output extraction order is:

1. non-empty `choices[0].message.content`;
2. `choices[0].message.tool_calls`;
3. non-empty legacy `choices[0].text`;
4. the full parsed response or raw text.

HTTP 4xx becomes a `FAILED` task with error text from `error.message`, `message`, or the raw body. HTTP 5xx is handled by dispatcher retries/fallbacks.

## Streaming response

The adapter processes `data:` lines in the Chat Completions stream. It emits `token_delta` only for non-empty `choices[0].delta.content`, treats `[DONE]` as normal completion, and converts a top-level `error` object into `task_error`.

Tool-call deltas and usage-only chunks are currently ignored. If your application needs streamed tool calls, extend the adapter and tests before relying on that behavior.

## OpenAI-compatible providers

You can point `endpoint` at another provider implementing the same Chat Completions request and response shapes. Model names, supported options, error payloads, and streaming extensions vary, so validate with that provider rather than assuming complete compatibility.

## Example call

```bash
curl -sS http://localhost:8080/api/v1/invoke/openai-chat \
  -H 'Authorization: Bearer your-proxy-key' \
  -H 'Content-Type: application/json' \
  -d '{"input":[{"role":"user","content":"Summarize retries in one sentence."}]}'
```
