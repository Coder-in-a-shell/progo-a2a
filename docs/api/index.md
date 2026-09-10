# API reference

ProGoA2A exposes three groups of HTTP routes:

| Surface | Routes | Purpose |
|---|---|---|
| A2A-inspired | `/a2a/v1/agents`, `/a2a/v1/tasks`, `/a2a/v1/tasks/stream`, `/a2a/v1/tasks/{task_id}` | Discovery, direct/capability routing, streaming, and stored result retrieval |
| Direct REST | `/api/v1/invoke/{agent_id}`, `/api/v1/stream/{agent_id}` | Simple point-to-point invocation |
| Operations | `/healthz`, `/readyz`, `/metrics` | Liveness, readiness, and Prometheus-format metrics |

- [A2A-inspired endpoints](a2a-endpoints.md)
- [Direct REST endpoints](rest-endpoints.md)
- [Health, metrics, and logs](observability.md)

## Common behavior

- Request and response bodies are JSON except for SSE responses and Prometheus text.
- When security is enabled, agent routes accept `Authorization: Bearer <key>` or `X-API-Key: <key>`.
- `/healthz`, `/readyz`, and `/metrics` are always public.
- Responses include `X-Request-ID`; an incoming value is preserved, otherwise the service generates a UUID.
- Errors use the `A2AError` JSON envelope documented in the endpoint guide.
