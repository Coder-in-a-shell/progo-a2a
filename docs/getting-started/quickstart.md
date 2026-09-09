# Quickstart

This example routes requests to the OpenAI Chat Completions endpoint. You can use the same gateway API with another adapter by changing the agent entry.

## 1. Build the service

```bash
git clone https://github.com/Coder-in-a-shell/progo-a2a.git
cd progo-a2a
make build
```

## 2. Create a minimal config

Save this as `progo-a2a.yaml`:

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  read_timeout_seconds: 30
  write_timeout_seconds: 120
  idle_timeout_seconds: 60

security:
  enabled: true
  api_keys:
    - key: "local-client-key"
      client_id: "quickstart"
      allowed_agents: ["openai-chat"]

agents:
  - id: "openai-chat"
    name: "OpenAI chat"
    description: "Quickstart upstream"
    type: "openai"
    endpoint: "https://api.openai.com/v1/chat/completions"
    capabilities: ["chat"]
    timeout_seconds: 60
    retries: 1
    fallback_agent_ids: []
    auth:
      type: "bearer"
      token: "${OPENAI_API_KEY}"
    options:
      model: "gpt-4o-mini"
```

Environment interpolation uses the exact `${NAME}` form. An unset variable becomes an empty string, so validate required secrets in your deployment environment.

## 3. Start the proxy

```bash
export OPENAI_API_KEY="your-upstream-key"
./bin/progo-a2a -config progo-a2a.yaml
```

Startup logs are JSON on stdout. A successful start includes messages for adapter registration and the listen address.

## 4. Invoke the agent

The A2A-inspired route accepts direct routing:

```bash
curl -sS http://localhost:8080/a2a/v1/tasks \
  -H 'Authorization: Bearer local-client-key' \
  -H 'Content-Type: application/json' \
  -d '{"agent_id":"openai-chat","input":"Reply with exactly: hello"}'
```

The response shape is:

```json
{
  "task_id": "task-0123456789abcdef",
  "agent_id": "openai-chat",
  "status": "COMPLETED",
  "output": "hello",
  "timestamp": "2026-09-10T00:00:00Z"
}
```

You can route by capability instead:

```bash
curl -sS http://localhost:8080/a2a/v1/tasks \
  -H 'Authorization: Bearer local-client-key' \
  -H 'Content-Type: application/json' \
  -d '{"required_capability":"chat","input":"Reply with exactly: hello"}'
```

Or use the direct REST route, where the agent ID is in the path:

```bash
curl -sS http://localhost:8080/api/v1/invoke/openai-chat \
  -H 'Authorization: Bearer local-client-key' \
  -H 'Content-Type: application/json' \
  -d '{"input":"Reply with exactly: hello"}'
```

## 5. Stream a response

```bash
curl -N -sS http://localhost:8080/a2a/v1/tasks/stream \
  -H 'Authorization: Bearer local-client-key' \
  -H 'Content-Type: application/json' \
  -d '{"agent_id":"openai-chat","input":"Write one short sentence."}'
```

The normalized SSE wire format looks like:

```text
event: task_started
data: {"agent_id":"openai-chat"}

event: token_delta
data: {"delta":"A short response."}

event: task_completed
data: {"status":"COMPLETED"}
```

## 6. Inspect operations endpoints

```bash
curl -sS http://localhost:8080/healthz
curl -sS http://localhost:8080/readyz
curl -sS http://localhost:8080/metrics | grep '^a2a_'
```

Health, readiness, and metrics are public even when `security.enabled` is true.

## Troubleshooting

| Symptom | Check |
|---|---|
| `401` | The inbound bearer token must match `security.api_keys[].key`. |
| `403` | The selected agent must appear in that key's `allowed_agents`, or use `"*"`. |
| `404` | Verify the configured `id` and requested agent/capability. |
| `502` | Check upstream reachability, credentials, endpoint shape, and adapter choice. |
| `504` | Increase `timeout_seconds` if the upstream legitimately needs longer. |

## Next steps

- [Configuration reference](../configuration/index.md)
- [Adapter guides](../adapters/index.md)
- [API reference](../api/index.md)
