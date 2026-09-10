# ProGoA2A

[![CI](https://github.com/Coder-in-a-shell/progo-a2a/actions/workflows/ci.yml/badge.svg)](https://github.com/Coder-in-a-shell/progo-a2a/actions/workflows/ci.yml)
[![Docs](https://github.com/Coder-in-a-shell/progo-a2a/actions/workflows/docs.yml/badge.svg)](https://coder-in-a-shell.github.io/progo-a2a/)
[![Go](https://img.shields.io/badge/Go-1.26.6+-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A fast, configurable Go gateway for invoking heterogeneous AI-agent HTTP APIs through one client interface.

ProGoA2A translates a common task request into payloads for LangGraph, CrewAI, AutoGen, OpenAI Chat Completions, or a custom webhook. It adds direct/capability routing, timeouts, jittered retries, ordered fallbacks, inbound API-key allowlists, normalized SSE, health endpoints, JSON logs, and Prometheus-format metrics.

> **Protocol status:** `/a2a/v1` is A2A-inspired. This release is not a complete implementation of the official A2A JSON-RPC protocol. The [protocol scope](https://coder-in-a-shell.github.io/progo-a2a/concepts/a2a-protocol/) documents the boundary precisely.

## Features

- Five adapters: `langgraph`, `crewai`, `autogen`, `openai`, and `custom`.
- Direct agent selection or first-match, case-insensitive capability routing.
- Per-attempt timeout, exponential backoff with jitter, and fallback graphs checked for cycles at startup.
- Synchronous JSON and streaming SSE routes.
- Inbound bearer/`X-API-Key` authentication with per-agent allowlists.
- Pluggable task storage: bounded in-memory cache (default 10,000 entries) or durable PostgreSQL storage (CI-tested with PostgreSQL 17).
- Request IDs, structured logs, health/readiness routes, and in-process metrics.
- Race-tested end-to-end suite and local microbenchmarks.

## Quick start

Requirements: Go 1.26.6 or newer, Git, and Make.

```bash
git clone https://github.com/Coder-in-a-shell/progo-a2a.git
cd progo-a2a
make build
```

Create `progo-a2a.yaml`:

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
    type: "openai"
    endpoint: "https://api.openai.com/v1/chat/completions"
    capabilities: ["chat"]
    timeout_seconds: 45
    retries: 1
    auth:
      type: "bearer"
      token: "${OPENAI_API_KEY}"
    options:
      model: "gpt-4o-mini"
```

Run it:

```bash
export OPENAI_API_KEY="your-upstream-key"
./bin/progo-a2a -config progo-a2a.yaml
```

Invoke the configured agent:

```bash
curl -sS http://localhost:8080/a2a/v1/tasks \
  -H 'Authorization: Bearer local-client-key' \
  -H 'Content-Type: application/json' \
  -d '{"agent_id":"openai-chat","input":"Reply with exactly: hello"}'
```

Or use capability routing:

```bash
curl -sS http://localhost:8080/a2a/v1/tasks \
  -H 'Authorization: Bearer local-client-key' \
  -H 'Content-Type: application/json' \
  -d '{"required_capability":"chat","input":"Reply with exactly: hello"}'
```

Stream with SSE:

```bash
curl -N -sS http://localhost:8080/a2a/v1/tasks/stream \
  -H 'Authorization: Bearer local-client-key' \
  -H 'Content-Type: application/json' \
  -d '{"agent_id":"openai-chat","input":"Write one short sentence."}'
```

## Routes

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/a2a/v1/agents` | List allowed configured agents |
| `GET` | `/a2a/v1/agents/{id}` | Read one agent card |
| `POST` | `/a2a/v1/tasks` | Synchronous direct/capability dispatch |
| `POST` | `/a2a/v1/tasks/stream` | Streaming direct/capability dispatch |
| `GET` | `/a2a/v1/tasks/{task_id}` | Read a stored completed result |
| `POST` | `/api/v1/invoke/{agent_id}` | Direct synchronous invocation |
| `POST` | `/api/v1/stream/{agent_id}` | Direct streaming invocation |
| `GET` | `/healthz`, `/readyz`, `/metrics` | Public operational routes |

## Docker

The repository builds an image locally; no official registry image or release binary is published yet. The default Docker Compose stack includes PostgreSQL 17 Alpine with health checks:

```bash
make docker-up      # docker compose up -d --build
make docker-down    # docker compose down (preserves database volume)
```

The checked-in Compose file uses placeholder upstream endpoints and secrets. See the [Docker guide](https://coder-in-a-shell.github.io/progo-a2a/deployment/docker/) and [PostgreSQL guide](https://coder-in-a-shell.github.io/progo-a2a/deployment/postgresql/) before real use.

## Development

```bash
make test   # go test -v -race ./...
go vet ./...
make build
make bench
```

To run PostgreSQL integration tests against a database reachable from the host:

```bash
POSTGRES_TEST_DSN="postgres://postgres:postgres_test_password@localhost:5432/progo_test?sslmode=disable" make test
```

On an Apple M2 with Go 1.26.5, a short local benchmark run measured roughly 42–47 µs/op for direct dispatcher calls and 57 µs/op through the task HTTP route. These tests use local mock servers; they are not production capacity guarantees.

Documentation uses MkDocs Material:

```bash
python3 -m venv .venv
.venv/bin/pip install -r requirements-docs.txt
.venv/bin/mkdocs serve
.venv/bin/mkdocs build --strict
```

## Production boundaries

Before production use, review the [production checklist](https://coder-in-a-shell.github.io/progo-a2a/deployment/production-checklist/). Important current limits include process-local metrics (task storage supports in-memory or PostgreSQL, but PostgreSQL does not add async jobs, stream persistence, cancellation, or automatic retention), no built-in TLS/rate limiting/circuit breaker, no stream heartbeats or resume support, and readiness that does not probe upstreams.

## Documentation

The full documentation is published at [coder-in-a-shell.github.io/progo-a2a](https://coder-in-a-shell.github.io/progo-a2a/).

- [Installation](https://coder-in-a-shell.github.io/progo-a2a/getting-started/installation/)
- [Configuration](https://coder-in-a-shell.github.io/progo-a2a/configuration/)
- [Adapters](https://coder-in-a-shell.github.io/progo-a2a/adapters/)
- [API reference](https://coder-in-a-shell.github.io/progo-a2a/api/)
- [Architecture](https://coder-in-a-shell.github.io/progo-a2a/concepts/architecture/)
- [PostgreSQL storage](https://coder-in-a-shell.github.io/progo-a2a/deployment/postgresql/)

## Contributing and license

See [CONTRIBUTING.md](CONTRIBUTING.md). ProGoA2A is available under the [MIT License](LICENSE).
