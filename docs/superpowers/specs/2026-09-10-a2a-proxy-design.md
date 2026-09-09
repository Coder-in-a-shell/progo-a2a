# A2A Proxy Design Specification

- **Date**: 2026-09-10
- **Status**: Approved
- **Author**: Antigravity Pair Programmer
- **Project Name**: `a2a-proxy`
- **Language & Runtime**: Go (v1.26+)

---

## 1. Executive Summary

The **A2A Proxy** is a high-performance, framework-agnostic Agent-to-Agent communication proxy written in Go. It enables client applications, orchestrators, and AI agents to discover, invoke, and stream tasks to diverse downstream agent services through a single unified interface. 

The proxy operates completely from a declarative configuration file (`a2a-proxy.yaml`). It bridges standard A2A protocols and generic REST calls to downstream agents running on disparate frameworks (such as LangGraph, CrewAI, AutoGen, OpenAI Assistant / Chat, or arbitrary in-house REST webhooks) without requiring custom code changes.

---

## 2. Goals and Non-Goals

### Goals
- **High Performance & Low Latency**: Sub-millisecond proxy routing overhead, minimal heap allocations per request, and efficient connection pooling via Go's `net/http`.
- **Framework Agnostic**: Native preset adapters for common agent frameworks alongside a fully declarative custom adapter supporting arbitrary JSON payloads via Go templates and JSONPath.
- **Dual Client Interface**:
  - Standard A2A Protocol: `/a2a/v1/agents` (discovery), `/a2a/v1/tasks` (execution), `/a2a/v1/tasks/stream` (SSE).
  - Unified REST API: `/api/v1/invoke/{agent_id}` and `/api/v1/stream/{agent_id}`.
- **Real-Time Streaming**: Full Server-Sent Events (SSE) support with per-framework stream chunk translation.
- **Resilience & Governance**: Per-agent timeout enforcement, exponential backoff retries, capability-based routing, and automatic fallback agents.
- **Security**: Inbound client API key authentication with agent-level RBAC; outbound credential injection supporting environment variable interpolation.
- **Observability**: Structured JSON logging (`log/slog`) with correlation trace IDs and Prometheus `/metrics`.

### Non-Goals
- **Agent Execution Engine**: The proxy is not an LLM agent executor itself; it intercepts, translates, governs, routes, and proxies to external agent services.
- **Stateful Database Requirement**: The proxy is designed to run stateless for maximum horizontal scalability. Dynamic session states are delegated to upstream agent frameworks or client task contexts.

---

## 3. System Architecture

```
[ Inbound Clients / Orchestrators ]
              │
              ▼
   ┌───────────────────────┐
   │      HTTP Server      │  (:8080)
   │  & Global Middleware  │  - Recovery & Request Correlation (trace_id)
   └──────────┬────────────┘  - Inbound Client Auth (API Key / RBAC)
              │               - Prometheus Request Metrics & Structured Logs
              ▼
   ┌───────────────────────┐
   │     Routing Layer     │  - Standard A2A Endpoints (/a2a/v1/...)
   │                       │  - Unified REST Endpoints (/api/v1/...)
   └──────────┬────────────┘
              │
              ▼
   ┌───────────────────────┐
   │  Dispatcher & Policy  │  - Direct Agent Lookup (by agent_id)
   │                       │  - Capability-Based Routing
   └──────────┬────────────┘  - Retries, Circuit Breaker & Fallback Chain
              │
              ▼
   ┌───────────────────────┐
   │  Translation Engine   │  - LangGraph Adapter
   │ (Pluggable Adapters)  │  - CrewAI Adapter
   │                       │  - AutoGen Adapter
   │                       │  - OpenAI Compatible Adapter
   │                       │  - Custom Declarative Adapter (Go Templates + JSONPath)
   └──────────┬────────────┘
              │
              ▼
   ┌───────────────────────┐
   │ Outbound HTTP Client  │  - Connection pooling (Keep-Alives)
   │                       │  - Outbound Auth / Secret Injection
   └──────────┬────────────┘
              │
              ▼
[ Downstream Agent Backends (LangGraph, CrewAI, AutoGen, Custom Webhooks) ]
```

---

## 4. Package Structure

```
a2a-proxy/
├── cmd/
│   └── proxy/
│       └── main.go                 # CLI entrypoint, flag parsing, graceful shutdown
├── config/
│   └── a2a-proxy.example.yaml      # Reference configuration template
├── pkg/
│   ├── config/
│   │   ├── config.go               # Configuration structs
│   │   ├── loader.go               # YAML parsing & env-var interpolation (${VAR})
│   │   └── validator.go            # Schema validation rules
│   ├── model/
│   │   ├── a2a.go                  # A2A protocol models (Agent, Task, Message, Artifact)
│   │   └── error.go                # Normalized error structures
│   ├── adapter/
│   │   ├── adapter.go              # Adapter interface definition
│   │   ├── registry.go             # Adapter factory & registry
│   │   ├── custom.go               # Declarative JSONPath / template adapter
│   │   ├── langgraph.go            # LangGraph preset
│   │   ├── crewai.go               # CrewAI preset
│   │   ├── autogen.go              # AutoGen preset
│   │   └── openai.go               # OpenAI Chat/Assistant preset
│   ├── dispatcher/
│   │   ├── dispatcher.go           # Agent routing, capability match, fallback logic
│   │   └── retry.go                # Exponential backoff and retry policy
│   ├── server/
│   │   ├── server.go               # HTTP server setup & lifecycle
│   │   ├── router.go               # Route registration
│   │   ├── a2a_handler.go          # Standard A2A spec endpoints
│   │   ├── rest_handler.go         # Direct REST endpoints
│   │   ├── health_handler.go       # /healthz and /readyz endpoints
│   │   └── middleware.go           # Auth, logger, metrics, recovery
│   ├── stream/
│   │   ├── sse.go                  # Server-Sent Events writer and flusher
│   │   └── emitter.go              # Stream event emitter interface
│   └── metrics/
│       └── metrics.go              # Prometheus collectors and metrics exporter
├── tests/
│   ├── mock/
│   │   └── mock_agents.go          # Mock upstream servers for LangGraph, CrewAI, etc.
│   └── e2e_test.go                 # End-to-end integration test suite
├── Makefile
├── go.mod
└── go.sum
```

---

## 5. Configuration Specification (`a2a-proxy.yaml`)

The proxy is configured via a YAML file. Environment variables matching `${VAR_NAME}` are expanded at startup.

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  read_timeout_seconds: 30
  write_timeout_seconds: 300
  idle_timeout_seconds: 120

security:
  enabled: true
  api_keys:
    - key: "client-key-production-1"
      client_id: "frontend-app"
      allowed_agents: ["*"]
    - key: "client-key-readonly-2"
      client_id: "analytics-bot"
      allowed_agents: ["researcher"]

agents:
  # LangGraph preset
  - id: "researcher"
    name: "Research Agent"
    description: "Performs web research and synthesizes findings"
    type: "langgraph"
    endpoint: "http://localhost:8000/runs/stream"
    capabilities: ["web_search", "summarization"]
    timeout_seconds: 60
    retries: 2
    auth:
      type: "bearer"
      token: "${LANGGRAPH_API_KEY}"
    fallback_agent_ids: ["researcher-backup"]

  # CrewAI preset
  - id: "writer"
    name: "Content Writer Agent"
    description: "Drafts documentation and blog posts"
    type: "crewai"
    endpoint: "http://localhost:8001/kickoff"
    capabilities: ["writing", "editing"]
    timeout_seconds: 90
    retries: 1

  # OpenAI Compatible preset
  - id: "researcher-backup"
    name: "OpenAI Fallback Researcher"
    description: "Backup researcher using direct OpenAI model"
    type: "openai"
    endpoint: "https://api.openai.com/v1/chat/completions"
    capabilities: ["web_search", "summarization"]
    timeout_seconds: 45
    auth:
      type: "bearer"
      token: "${OPENAI_API_KEY}"
    options:
      model: "gpt-4o"

  # Custom Declarative Framework-Agnostic Agent
  - id: "internal-support"
    name: "Support Webhook"
    description: "Internal customer support microservice"
    type: "custom"
    endpoint: "https://support.corp.internal/v1/query"
    capabilities: ["customer_support"]
    timeout_seconds: 30
    auth:
      type: "header"
      header_name: "X-Internal-Token"
      header_value: "${SUPPORT_TOKEN}"
    mapping:
      request:
        method: "POST"
        headers:
          "Content-Type": "application/json"
          "X-Client-Source": "a2a-proxy"
        body_template: |
          {
            "query_text": {{ .Task.Input | toJson }},
            "caller_id": {{ .Task.ID | toJson }},
            "session_context": {{ .Task.Context | toJson }}
          }
      response:
        output_path: "$.result.answer"
        status_path: "$.result.state"
        artifacts_path: "$.result.attachments"
        error_path: "$.error.message"
      stream:
        data_path: "$.delta.text"
        done_sentinel: "[DONE]"
```

---

## 6. Adapter Translation Engine

### 6.1 Interface Definition

```go
package adapter

import (
    "context"
    "net/http"
    "a2a-proxy/pkg/config"
    "a2a-proxy/pkg/model"
    "a2a-proxy/pkg/stream"
)

type Adapter interface {
    Type() string
    TranslateRequest(ctx context.Context, agent *config.AgentConfig, task *model.TaskRequest) (*http.Request, error)
    TranslateResponse(ctx context.Context, agent *config.AgentConfig, resp *http.Response) (*model.TaskResponse, error)
    TranslateStream(ctx context.Context, agent *config.AgentConfig, resp *http.Response, emitter stream.Emitter) error
}
```

### 6.2 Declarative Custom Adapter
The `CustomAdapter` interprets YAML templates and JSONPath selectors:
- **Template Functions**:
  - `toJson`: marshals any Go value to JSON string.
  - `default`: returns fallback if string is empty.
  - `env`: reads system environment variable.
- **JSONPath Engine**:
  - Evaluates expressions like `$.result.answer` or `$.choices[0].message.content`.
  - Maps downstream status codes/strings into canonical A2A status: `COMPLETED`, `FAILED`, `IN_PROGRESS`.

---

## 7. Protocol API Endpoints

### 7.1 Standard A2A Protocol Endpoints
- `GET /a2a/v1/agents`: Returns list of all configured agents, their descriptions, and capabilities.
- `GET /a2a/v1/agents/{agent_id}`: Returns metadata for a specific agent.
- `POST /a2a/v1/tasks`: Submits a task. Body:
  ```json
  {
    "agent_id": "researcher",
    "input": "Summarize the latest developments in AI protocols.",
    "context": { "user_id": "user-123" }
  }
  ```
  Returns `200 OK` with `TaskResponse` or `202 Accepted` for async tasks.
- `POST /a2a/v1/tasks/stream`: Streaming task execution. Client receives `text/event-stream`.
- `GET /a2a/v1/tasks/{task_id}`: Polls status of a running or completed task.

### 7.2 Unified REST Endpoints
- `POST /api/v1/invoke/{agent_id}`: Direct task invocation by agent ID.
- `POST /api/v1/stream/{agent_id}`: Direct task streaming by agent ID.

### 7.3 Operational Endpoints
- `GET /healthz`: Liveness probe.
- `GET /readyz`: Readiness probe (checks config validity and adapter status).
- `GET /metrics`: Standard Prometheus metrics.

---

## 8. Resilience, Routing, and Error Handling

### 8.1 Capability-Based Routing
When `agent_id` is empty in `TaskRequest`, the caller can provide `"required_capability": "web_search"`. The dispatcher filters the registered agents for that capability, selects a healthy candidate, and proceeds with dispatch.

### 8.2 Retries and Fallbacks
1. Upon network failure or HTTP 5xx from downstream agent, retry up to `retries` times with exponential backoff (initial 100ms, max 2s).
2. If retries are exhausted and `fallback_agent_ids` are defined, dispatch the request to the first fallback agent.
3. If all fail, return a structured `A2AError`:
```json
{
  "error": {
    "code": "DOWNSTREAM_UNAVAILABLE",
    "message": "All attempts to invoke downstream agents failed",
    "agent_id": "researcher",
    "status": 502,
    "timestamp": "2026-09-10T01:18:45Z"
  }
}
```

---

## 9. Verification & Test Plan

1. **Unit Tests**:
   - `pkg/config`: Verify YAML deserialization, required field checks, and `${ENV}` expansion.
   - `pkg/adapter`: Verify translation accuracy for `langgraph`, `crewai`, `autogen`, `openai`, and `custom` adapters against fixture payloads.
   - `pkg/dispatcher`: Verify capability lookup, retry counters, and fallback cascading.
2. **Integration Tests (`tests/e2e_test.go`)**:
   - Spin up `httptest.Server` mocks simulating LangGraph, CrewAI, and custom webhook backends.
   - Execute synchronous `/a2a/v1/tasks` calls and verify response normalization.
   - Execute streaming `/a2a/v1/tasks/stream` SSE calls and verify event sequencing (`task_started` -> `token_delta` -> `task_completed`).
   - Test authentication failures (missing/invalid key -> 401 Unauthorized, unpermitted agent -> 403 Forbidden).
3. **Benchmarking**:
   - Benchmark throughput and allocation overhead with `go test -bench=. -benchmem`.
