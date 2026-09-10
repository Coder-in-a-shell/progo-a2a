# Configuration Reference

Complete reference for every field in `progo-a2a.yaml`.

---

## Overview

ProGoA2A is configured via a single YAML file. By default the binary loads `config/progo-a2a.example.yaml`. Override the path with the `-config` flag:

```bash
./progo-a2a -config /etc/progo-a2a/config.yaml
```

The file has top-level configuration options and sections:

| Field / Section | Required | Purpose |
|---|---|---|
| `role` | No | Runtime execution mode (`api`, `worker`, or `all`; default `api`) |
| `server` | Yes | HTTP listener settings |
| `worker` | No | Background worker settings for durable jobs |
| `storage` | No | Pluggable task storage (`memory` or `postgres`) |
| `security` | No | API-key authentication & RBAC |
| `agents` | Yes | Upstream agent definitions |

---

## `server` Section

Controls the HTTP server that ProGoA2A exposes to clients.

```yaml
server:
  host: "0.0.0.0"           # (1)
  port: 8080                 # (2)
  read_timeout_seconds: 30   # (3)
  write_timeout_seconds: 120 # (4)
  idle_timeout_seconds: 60   # (5)
```

| Field | Type | Default | Description |
|---|---|---|---|
| `host` | string | `"0.0.0.0"` | Bind address. Use `"127.0.0.1"` to restrict to localhost. |
| `port` | integer | `8080` | TCP port to listen on. |
| `read_timeout_seconds` | integer | `30` | Maximum time to read an entire request (headers + body). Protects against slow-loris attacks. |
| `write_timeout_seconds` | integer | `120` | Maximum time to write a non-streaming response. Streaming handlers clear this deadline. Account for upstream attempts, backoff, and fallbacks when sizing it. |
| `idle_timeout_seconds` | integer | `60` | Keep-alive idle timeout. How long an idle connection is kept open waiting for the next request. |

!!! tip "Sizing `write_timeout_seconds`"
    A request can consume multiple agent attempts plus backoff and fallback attempts. Set this timeout from your end-to-end latency budget, not only the largest single `timeout_seconds` value.

---

## Runtime Roles (`role`)

The top-level `role` field configures the execution mode of the binary:

```yaml
role: "api" # "api" | "worker" | "all" (default: "api")
```

| Role | Description | Requirements |
|---|---|---|
| `api` | Starts only the HTTP server exposing client endpoints. Preserves existing server-only behavior. | Compatible with `memory` or `postgres` storage. |
| `worker` | Starts only background worker engine processing durable jobs from PostgreSQL. | Requires `storage.backend: postgres`. |
| `all` | Starts both HTTP server and background worker engine in a single process. | Requires `storage.backend: postgres` (shares a single pool). |

Override the role at runtime with the `-role` CLI flag:
```bash
./progo-a2a -config config/progo-a2a.yaml -role worker
```

---

## `worker` Section

Configures the durable worker engine when running in `worker` or `all` roles.

```yaml
worker:
  worker_id: "worker-prod-1"         # optional stable worker identifier (non-empty cannot be whitespace-only)
  concurrency: 10                     # integer, 1 to 1000 (default: 10)
  batch_size: 5                       # integer, 1 to concurrency (default: 5)
  poll_interval_milliseconds: 1000    # integer, 1 to 600000 (default: 1000)
  lease_duration_seconds: 30          # integer, 2 to 86400 (default: 30)
  renewal_interval_seconds: 10        # integer, > 0 and <= lease_duration / 2 (default: 10)
  retry_backoff_seconds: 15           # integer, > 0 and <= 604800 (default: 15)
  drain_timeout_seconds: 30           # integer, 1 to 3600 (default: 30)
```

| Field | Type | Default | Bounds / Constraints | Description |
|---|---|---|---|---|
| `worker_id` | string | `""` | Maximum 256 characters; cannot be whitespace-only | Optional stable worker identifier. If omitted, generated as `hostname-pid-timestamp`. |
| `concurrency` | integer | `10` | `1` to `1000` | Maximum number of concurrent jobs processed simultaneously. |
| `batch_size` | integer | `5` | `1` to `min(concurrency, 1000)` | Maximum jobs claimed per lease acquisition attempt. |
| `poll_interval_milliseconds` | integer | `1000` | `1` to `600000` (10m) | Polling sleep interval when no queued jobs are available. |
| `lease_duration_seconds` | integer | `30` | `2` to `86400` (24h) | Distributed lease duration claimed in database time. The two-second minimum permits a positive integer renewal interval at or below half the lease. |
| `renewal_interval_seconds` | integer | `10` | `> 0` and `<= lease_duration_seconds / 2` | Heartbeat interval at which running job leases are extended (must be at most half the lease duration). |
| `retry_backoff_seconds` | integer | `15` | `> 0` and `<= 604800` (7d) | Database-time delay before a retryable job becomes eligible again. Expired-lease sweeps run independently at the renewal interval. |
| `drain_timeout_seconds` | integer | `30` | `1` to `3600` (1h) | Maximum duration allowed for active jobs to finish during graceful shutdown before canceling contexts. |

Each durable job lease makes one downstream invocation. The worker does not use the legacy synchronous dispatcher's per-agent retry or fallback loop; durable retries occur only through `max_attempts` and database-time `retry_backoff_seconds`.

---

## `storage` Section

Configures the storage backend for completed synchronous task responses. ProGoA2A supports two backends: `memory` (default in-process FIFO cache) and `postgres` (shared durable PostgreSQL storage). See the [PostgreSQL deployment guide](../deployment/postgresql.md) for architecture and operational details.

```yaml
storage:
  backend: "memory"           # "memory" | "postgres" (default: "memory")

  # In-memory settings (valid ONLY when backend is "memory")
  memory:
    max_tasks: 10000          # integer, 0 uses the 10000 default; maximum 1000000

  # PostgreSQL settings (valid ONLY when backend is "postgres")
  postgres:
    dsn: "postgres://user:pass@host:5432/dbname?sslmode=require"
    max_connections: 20       # integer, bounds: 1 to 1000 (default: 20)
    min_connections: 2        # integer, bounds: 0 to max_connections (default: 2)
    max_connection_lifetime_seconds: 1800 # integer, > 0 (default: 1800)
    max_connection_idle_time_seconds: 300 # integer, > 0 (default: 300)
    health_check_period_seconds: 30       # integer, > 0 (default: 30)
    connect_timeout_seconds: 5            # integer, > 0 (default: 5)
    migrate_on_start: false               # boolean (default: false)
```

| Field | Type | Default | Bounds / Constraints | Description |
|---|---|---|---|---|
| `backend` | string | `"memory"` | `"memory"` \| `"postgres"` | Storage backend selection. |
| `memory.max_tasks` | integer | `10000` | `0` uses default; maximum `1000000` | Maximum number of task responses retained in memory before FIFO eviction. |
| `postgres.dsn` | string | `""` | Non-empty when `backend: postgres` | PostgreSQL connection DSN. Supports `${DATABASE_URL}` references. |
| `postgres.max_connections` | integer | `20` | `1` to `1000` | Maximum open connections in the pgx connection pool. |
| `postgres.min_connections` | integer | `2` | `0` to `max_connections` | Minimum idle connections pre-warmed in the pool. |
| `postgres.max_connection_lifetime_seconds` | integer | `1800` | `> 0` | Maximum lifetime of a connection before being closed and recycled. |
| `postgres.max_connection_idle_time_seconds` | integer | `300` | `> 0` | Maximum duration an idle connection remains open. |
| `postgres.health_check_period_seconds` | integer | `30` | `> 0` | Interval at which health checks ping idle connections. |
| `postgres.connect_timeout_seconds` | integer | `5` | `> 0` | Context timeout for establishing initial database connections and startup ping. |
| `postgres.migrate_on_start` | boolean | `false` | `true` \| `false` | When `true`, automatically applies embedded idempotent migrations with advisory locking on startup. |

!!! warning "Mutual Exclusion"
    The configuration validator enforces strict separation between backend configurations:

    - When `backend: memory` (or empty), configuring any `postgres` settings or a non-empty `postgres.dsn` causes startup validation to fail.
    - When `backend: postgres`, configuring any non-zero `memory` settings causes startup validation to fail.
    - In dual-mode configuration files (like `config/progo-a2a.example.yaml`), omit explicit pool and memory settings to allow the loader to inject backend-specific defaults dynamically.

---

## `security` Section

When `enabled: true`, agent and discovery requests must carry a valid `Authorization: Bearer <token>` or `X-API-Key: <token>` header. `/healthz`, `/readyz`, and `/metrics` remain public. Access is controlled per key via `allowed_agents`.

```yaml
security:
  enabled: true         # (1)
  api_keys:             # (2)
    - key: "secret-token"       # (3)
      client_id: "my-client"    # (4)
      allowed_agents:           # (5)
        - "*"                   # (6) wildcard - all agents
        - "specific-agent-id"   # (7) or list individual IDs
```

| Field | Type | Required | Description |
|---|---|---|---|
| `enabled` | boolean | No (default `false`) | Globally enable/disable authentication. When `false`, all requests are accepted without a token. |
| `api_keys` | list | When `enabled: true` | List of valid API key definitions. |
| `api_keys[].key` | string | Yes | The raw secret token clients must present. Use `${ENV_VAR}` references — never hardcode in source control. |
| `api_keys[].client_id` | string | Recommended | Human-readable identifier attached to the request context. The current request logger does not emit it. |
| `api_keys[].allowed_agents` | list of strings | Yes | Agent IDs this key may access. Use `"*"` for unrestricted access, or list specific agent IDs. |

!!! warning "Security note"
    Token comparison uses `crypto/subtle.ConstantTimeCompare` to prevent timing attacks. See the [Security & RBAC](security-rbac.md) page for the full security model.

---

## `agents` Section

Defines every upstream agent ProGoA2A can route to. Each entry is a full agent definition.

### Full Agent Schema

```yaml
agents:
  - id: "unique-agent-id"        # (1)  Required
    name: "Human Name"           # (2)  Optional
    description: "What it does"  # (3)  Optional
    type: "langgraph"            # (4)  Required
    endpoint: "http://..."       # (5)  Required
    capabilities: []             # (6)  Optional
    tags: []                     # (7)  Optional
    timeout_seconds: 60          # (8)  Default: 60
    retries: 2                   # (9)  Default: 0
    fallback_agent_ids: []       # (10) Optional
    auth:                        # (11) Optional
      type: "bearer"             # bearer | header | none
      token: "${MY_ENV_VAR}"     # For bearer type
      header_name: "X-Token"    # For header type
      header_value: "val"        # For header type
    options: {}                  # (12) Adapter-specific options
    mapping:                     # (13) Only for type: custom
      request:
        method: "POST"
        headers: {}
        body_template: ""        # Go text/template string
      response:
        output_path: "$.field"   # JSONPath
        status_path: "$.status"
        artifacts_path: "$.artifacts"
        error_path: "$.error"
      stream:
        data_path: "$.delta.text"  # JSONPath into each SSE event data
        done_sentinel: "[DONE]"    # String that signals end of stream
```

### Field Reference

#### Identity

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | **Yes** | Unique identifier used in API calls and RBAC rules. The validator checks presence and uniqueness, but does not currently enforce a URL-safe character set. |
| `name` | string | No | Human-readable display name. Used in logs and the agent card endpoint. |
| `description` | string | No | Free-text description of the agent's purpose. Returned in the agent card. |

#### Routing

| Field | Type | Required | Description |
|---|---|---|---|
| `type` | string | **Yes** | Adapter type. One of: `langgraph`, `crewai`, `autogen`, `openai`, `custom`. |
| `endpoint` | string | **Yes** | Full URL of the upstream agent (e.g. `http://localhost:8000/runs/wait`). |
| `capabilities` | list of strings | No | Semantic capability tags (e.g. `["research", "code-gen"]`). Used for capability-based routing. |
| `tags` | list of strings | No | Arbitrary metadata tags. Not used for routing; available via the agent card. |

#### Reliability

| Field | Type | Default | Description |
|---|---|---|---|
| `timeout_seconds` | integer | `60` | Per-attempt HTTP timeout when calling the upstream agent. Does not include backoff or later attempts. |
| `retries` | integer | `0` | Number of additional retry attempts on failure. A value of `2` means up to 3 total attempts. |
| `fallback_agent_ids` | list of strings | `[]` | Ordered list of agent IDs to try if all attempts to this agent fail. Each fallback is tried in sequence. |

#### Upstream Auth (`auth`)

| Field | Type | Description |
|---|---|---|
| `auth.type` | string | `bearer` — adds `Authorization: Bearer <token>` header. `header` — adds a custom header. `none` (default) — no auth header. |
| `auth.token` | string | The bearer token value. Use `${ENV_VAR}`. Only used when `type: bearer`. |
| `auth.header_name` | string | Header name to inject. Only used when `type: header`. |
| `auth.header_value` | string | Header value. Use `${ENV_VAR}`. Only used when `type: header`. |

#### Adapter Options (`options`)

Free-form map passed to the adapter. Fields vary by `type`. See the individual adapter pages:

- [LangGraph Adapter](../adapters/langgraph.md)
- [CrewAI Adapter](../adapters/crewai.md)
- [AutoGen Adapter](../adapters/autogen.md)
- [OpenAI Adapter](../adapters/openai.md)
- [Custom Webhooks](../adapters/custom-webhooks.md)

#### Custom Mapping (`mapping`) — `type: custom` only

The `mapping` block is only evaluated when `type: custom`. It configures how the proxy translates A2A tasks into upstream HTTP requests and back.

=== "Request mapping"

    | Field | Type | Description |
    |---|---|---|
    | `mapping.request.method` | string | HTTP method (`POST`, `GET`, `PUT`, etc.) |
    | `mapping.request.headers` | map | Static headers to include in every upstream request. |
    | `mapping.request.body_template` | string | Go `text/template` string. Template variables: `.Task.ID`, `.Task.Input`, `.Task.Context`, `.Task.AgentID`. |

=== "Response mapping"

    | Field | Type | Description |
    |---|---|---|
    | `mapping.response.output_path` | string | JSONPath (dot notation, e.g. `$.data.answer`) to extract the text output. |
    | `mapping.response.status_path` | string | JSONPath to extract the status string. |
    | `mapping.response.artifacts_path` | string | JSONPath to extract an artifacts array. |
    | `mapping.response.error_path` | string | Path used to extract an error message for HTTP errors or when `status_path` maps to `FAILED`. It does not cause failure by itself. |

=== "Stream mapping"

    | Field | Type | Description |
    |---|---|---|
    | `mapping.stream.data_path` | string | JSONPath applied to each SSE event's JSON payload to extract the text delta. |
    | `mapping.stream.done_sentinel` | string | A string value in the SSE `data:` field that signals end-of-stream (e.g. `[DONE]`). |

---

## ENV Variable Interpolation

ProGoA2A supports `${VAR_NAME}` placeholders anywhere in string values. Expansion happens **at startup** using regex substitution before YAML is parsed into structs.

### Syntax

```yaml
auth:
  type: "bearer"
  token: "${OPENAI_API_KEY}"       # Replaced at startup

api_keys:
  - key: "${MY_API_KEY}"           # Works in security section too
    client_id: "my-service"
```

### How it works

1. The raw YAML file is read as bytes.
2. All occurrences of `${VAR_NAME}` are replaced with `os.Getenv("VAR_NAME")`.
3. The substituted string is then parsed as YAML.

This means substitution is purely textual — the replaced value must produce valid YAML.

### Examples

```bash
# Set in environment before starting the proxy
export OPENAI_API_KEY="sk-..."
export LANGGRAPH_API_KEY="lgapi-..."
export ADMIN_API_KEY="super-secret-$(openssl rand -hex 16)"

./progo-a2a -config progo-a2a.yaml
```

!!! warning "Missing variables"
    If a referenced environment variable is **not set**, the placeholder is replaced with an **empty string**. Empty inbound API keys are rejected when security is enabled; missing upstream credentials may not fail until invocation. Always verify required variables before starting the proxy.

!!! danger "Never commit secrets"
    Do not hardcode API keys or tokens directly in your YAML file. Always use `${ENV_VAR}` references and manage secrets via your platform's secret management system (Kubernetes Secrets, Vault, AWS Secrets Manager, etc.).

---

## Full Annotated Example

The following is a complete, production-representative configuration file demonstrating all major features:

```yaml
# progo-a2a.example.yaml
# ProGoA2A full example configuration

server:
  host: "0.0.0.0"
  port: 8080
  read_timeout_seconds: 30
  write_timeout_seconds: 120   # Must exceed longest agent timeout
  idle_timeout_seconds: 60

security:
  enabled: true
  api_keys:
    # Admin key: full access to all agents
    - key: "${ADMIN_API_KEY}"
      client_id: "admin-client"
      allowed_agents:
        - "*"

    # Analyst key: restricted to specific agents
    - key: "${ANALYST_API_KEY}"
      client_id: "analyst-client"
      allowed_agents:
        - "langgraph-researcher"
        - "openai-gpt4o"

agents:
  # --- LangGraph agent (Python state-machine agent) ---
  - id: "langgraph-researcher"
    name: "LangGraph Research Agent"
    type: "langgraph"
    endpoint: "http://localhost:8000/runs/wait"
    capabilities: ["research", "deep-search"]
    timeout_seconds: 60
    retries: 2
    fallback_agent_ids: ["openai-gpt4o"]   # Fall back to GPT-4o on failure
    auth:
      type: "bearer"
      token: "${LANGGRAPH_API_KEY}"
    options:
      assistant_id: "researcher"            # LangGraph assistant name (required)

  # --- OpenAI-compatible agent ---
  - id: "openai-gpt4o"
    name: "OpenAI GPT-4o"
    type: "openai"
    endpoint: "https://api.openai.com/v1/chat/completions"
    timeout_seconds: 45
    retries: 3
    auth:
      type: "bearer"
      token: "${OPENAI_API_KEY}"
    options:
      model: "gpt-4o"
      temperature: 0.7
      max_tokens: 4096

  # --- Custom webhook agent (arbitrary REST endpoint) ---
  - id: "custom-enterprise-agent"
    name: "Enterprise KB"
    type: "custom"
    endpoint: "https://agent.enterprise.internal/v1/execute"
    auth:
      type: "header"
      header_name: "X-Enterprise-Auth"
      header_value: "${ENTERPRISE_AUTH_TOKEN}"
    mapping:
      request:
        method: "POST"
        headers:
          Content-Type: "application/json"
        body_template: >-
          {"task_id": {{ .Task.ID | toJson }},
           "query": {{ .Task.Input | toJson }}}
      response:
        output_path: "$.result.answer"
        error_path: "$.error.message"
      stream:
        data_path: "$.delta.text"
        done_sentinel: "[DONE]"
```

---

## Quick-Reference Table

| Field | Default | Notes |
|---|---|---|
| `server.host` | `"0.0.0.0"` | |
| `server.port` | `8080` | |
| `server.read_timeout_seconds` | `30` | |
| `server.write_timeout_seconds` | `120` | Must exceed agent timeouts |
| `server.idle_timeout_seconds` | `60` | |
| `storage.backend` | `"memory"` | `"memory"` \| `"postgres"` |
| `storage.memory.max_tasks` | `10000` | Bounds: 0 to 1,000,000 |
| `storage.postgres.dsn` | `""` | Required for postgres backend |
| `storage.postgres.max_connections` | `20` | Bounds: 1 to 1,000 |
| `storage.postgres.min_connections` | `2` | Bounds: 0 to max_connections |
| `storage.postgres.max_connection_lifetime_seconds` | `1800` | Recycling interval |
| `storage.postgres.max_connection_idle_time_seconds` | `300` | Idle timeout |
| `storage.postgres.health_check_period_seconds` | `30` | Background ping interval |
| `storage.postgres.connect_timeout_seconds` | `5` | Startup dial/ping timeout |
| `storage.postgres.migrate_on_start` | `false` | Run embedded migrations on startup |
| `security.enabled` | `false` | |
| `agent.timeout_seconds` | `60` | Per-attempt |
| `agent.retries` | `0` | Additional attempts |
| `agent.auth.type` | `none` | `bearer` \| `header` \| `none` |
