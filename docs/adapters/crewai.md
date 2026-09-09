# CrewAI adapter

Use `type: crewai` for a CrewAI-facing service that accepts a kickoff-style JSON request. CrewAI itself can be exposed through different APIs; verify this shape against your service.

## Request translation

If `input` is a JSON object, it becomes `inputs` directly:

```json
{"inputs":{"topic":"resilient systems","audience":"engineers"}}
```

For any other input type:

```json
{"inputs":{"input":"Write an outline"}}
```

Every key in `options` except `inputs` is copied to the top-level payload.

```yaml
agents:
  - id: crew-writer
    name: Crew writer
    type: crewai
    endpoint: http://localhost:5000/kickoff
    capabilities: [writing]
    timeout_seconds: 90
    retries: 1
    auth:
      type: header
      header_name: X-Crew-Token
      header_value: "${CREWAI_API_TOKEN}"
    options:
      crew_name: technical-docs
```

The adapter sends the same endpoint for sync and stream requests. It adds `Accept: text/event-stream` for the latter.

## Synchronous response

Output extraction order is:

1. `result`;
2. `raw`;
3. `output`;
4. `tasks_output`;
5. the whole parsed JSON body or raw text.

If top-level `status` is `failed` or `error`, the normalized task becomes `FAILED`. Error text prefers `error.message`, `error`, then `message`. HTTP 4xx responses also become failed task responses; HTTP 5xx responses are handled by dispatcher retry/fallback.

## Streaming response

The adapter processes `data:` lines until `[DONE]` or EOF:

- JSON containing `error` emits `task_error` and stops.
- `thought`, `action`, or `step` emits `step_progress`.
- `delta` or `output` emits `token_delta`.
- Other JSON and plain text also emit `token_delta`.

`mapping.stream` is not used by this adapter; use `type: custom` if your upstream needs configurable extraction.

## Troubleshooting

| Symptom | Check |
|---|---|
| Empty or surprising output | Compare the response with the extraction order above. |
| No stream chunks | Confirm the endpoint emits SSE `data:` lines. |
| Repeated calls | `retries` applies to transport errors and HTTP 5xx. |
| `403` from upstream | Verify the configured outbound header and environment value. |
