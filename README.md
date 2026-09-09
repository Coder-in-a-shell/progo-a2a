# ProGoA2A

<p align="center">
  <br>
  <strong>High-performance, framework-agnostic Agent-to-Agent (A2A) proxy in Go.</strong>
  <br>
  <sub>Connect, translate, govern, and stream between disparate AI agents and frameworks with a single declarative configuration.</sub>
  <br><br>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go" alt="Go Version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License: MIT"></a>
  <a href="https://github.com/Coder-in-a-shell/progo-a2a/actions"><img src="https://img.shields.io/badge/CI-Passing-brightgreen.svg" alt="CI Status"></a>
  <a href="https://github.com/Coder-in-a-shell/progo-a2a/pulls"><img src="https://img.shields.io/badge/PRs-welcome-brightgreen.svg" alt="PRs Welcome"></a>
  <a href="#benchmarks"><img src="https://img.shields.io/badge/Latency-~47µs-success" alt="Benchmark Latency"></a>
</p>

---

## ⚡ Overview

**ProGoA2A** is an ultra-fast, single-binary proxy designed for the emerging **Agent-to-Agent (A2A)** ecosystem. 

As autonomous AI agents multiply across different frameworks (LangGraph, CrewAI, AutoGen, OpenAI, or custom proprietary APIs), orchestrating them becomes a chaotic web of incompatible JSON schemas and streaming formats. 

ProGoA2A solves this by sitting in the middle as a **declarative, high-speed proxy**:
- **Framework Agnostic**: Native preset adapters for **LangGraph**, **CrewAI**, **AutoGen**, and **OpenAI**, plus a **Declarative Custom Adapter** (Go Templates + JSONPath) for any arbitrary REST agent.
- **Dual Client Interface**: Call agents via the **Standard A2A Protocol** (`/a2a/v1/tasks`) or a **Unified REST API** (`/api/v1/invoke/{id}`).
- **Real-Time SSE Streaming**: Translates downstream streaming chunks into unified Server-Sent Events in real time.
- **Production Hardened**: Inbound API key auth with agent-level RBAC, DoS request body limits, attempt-scoped timeouts, exponential retries with jitter, automatic fallback failover, and Prometheus metrics.
- **Blazing Fast**: Sub-millisecond dispatch (~47 µs) and sub-microsecond translation (~290 ns – 2 µs) handling 20,000+ requests/sec per core.

---

## 🏗️ Architecture

```mermaid
flowchart TD
    Client["Client / Agent Orchestrator"]
    
    subgraph ProGoA2A ["ProGoA2A Gateway (:8080)"]
        MW["Middleware Chain<br/>(Panic Recovery, Tracing, Logging, Metrics, RBAC)"]
        Router{"Router<br/>/a2a/v1/* & /api/v1/*"}
        Dispatcher["Resilient Dispatcher<br/>(Timeouts, Jitter Retries, Fallbacks)"]
        
        subgraph Adapters ["Pluggable Translation Adapters"]
            LG["LangGraph Adapter"]
            Crew["CrewAI Adapter"]
            Auto["AutoGen Adapter"]
            OAI["OpenAI Adapter"]
            Custom["Declarative Custom Adapter<br/>(Go Templates + JSONPath)"]
        end
        
        ConnPool["HTTP Connection Pool<br/>(MaxIdleConns: 100, Keep-Alive)"]
    end
    
    subgraph Backends ["Downstream Agent Backends"]
        LGServer["LangGraph Service"]
        CrewServer["CrewAI Service"]
        AutoServer["AutoGen Service"]
        OAIServer["OpenAI API"]
        CustomServer["In-House Webhooks"]
    end

    Client -->|HTTP / SSE| MW
    MW --> Router
    Router --> Dispatcher
    Dispatcher --> Adapters
    Adapters --> ConnPool
    ConnPool --> LGServer
    ConnPool --> CrewServer
    ConnPool --> AutoServer
    ConnPool --> OAIServer
    ConnPool --> CustomServer
```

---

## 🚀 Quick Start in 60 Seconds

### 1. Run via Docker Compose
```bash
git clone https://github.com/Coder-in-a-shell/progo-a2a.git
cd progo-a2a

docker-compose up -d
```

### 2. Or Build from Source (Go 1.22+)
```bash
make build
./bin/progo-a2a -config config/progo-a2a.example.yaml
```

### 3. Verify Health & Discovery
```bash
# Check Health
curl http://localhost:8080/healthz

# Discover Configured Agents
curl -H "Authorization: Bearer client-key-production-1" \
  http://localhost:8080/a2a/v1/agents
```

---

## ⚙️ Declarative Configuration (`progo-a2a.yaml`)

ProGoA2A is 100% config-driven. Environment variables like `${OPENAI_API_KEY}` are expanded at startup:

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  read_timeout_seconds: 30
  write_timeout_seconds: 120

security:
  enabled: true
  api_keys:
    - key: "my-secret-client-token"
      client_id: "frontend-orchestrator"
      allowed_agents: ["*"]  # Wildcard or specific agent IDs

agents:
  # 1. Native LangGraph Agent
  - id: "researcher"
    name: "Research Agent"
    type: "langgraph"
    endpoint: "http://localhost:8000/runs/wait"
    capabilities: ["web_search", "summarization"]
    timeout_seconds: 60
    retries: 2
    auth:
      type: "bearer"
      token: "${LANGGRAPH_API_KEY}"
    fallback_agent_ids: ["researcher-backup"]

  # 2. Native OpenAI Agent (Backup Fallback)
  - id: "researcher-backup"
    name: "OpenAI Fallback"
    type: "openai"
    endpoint: "https://api.openai.com/v1/chat/completions"
    timeout_seconds: 45
    auth:
      type: "bearer"
      token: "${OPENAI_API_KEY}"
    options:
      model: "gpt-4o"

  # 3. Completely Custom / Proprietary Agent
  - id: "support-webhook"
    name: "Customer Support Webhook"
    type: "custom"
    endpoint: "https://internal-support.corp.net/v2/ask"
    capabilities: ["customer_support"]
    auth:
      type: "header"
      header_name: "X-Internal-Token"
      header_value: "${SUPPORT_TOKEN}"
    mapping:
      request:
        method: "POST"
        headers:
          "Content-Type": "application/json"
        body_template: |
          {
            "query": {{ .Task.Input | toJson }},
            "session_id": {{ .Task.ID | toJson }},
            "caller_context": {{ .Task.Context | toJson }}
          }
      response:
        output_path: "$.data.answer"
        status_path: "$.data.state"
        error_path: "$.error.message"
      stream:
        data_path: "$.delta.text"
        done_sentinel: "[DONE]"
```

---

## 📡 API Reference

### 1. Standard A2A Protocol Endpoints

#### Discover Available Agents
```http
GET /a2a/v1/agents
Authorization: Bearer <API_KEY>
```

#### Execute a Task
```http
POST /a2a/v1/tasks
Authorization: Bearer <API_KEY>
Content-Type: application/json

{
  "agent_id": "researcher",
  "input": "Summarize the latest trends in autonomous agents.",
  "context": {
    "user_id": "user-42"
  }
}
```

#### Capability-Based Routing
Omit `agent_id` and specify `required_capability`. ProGoA2A automatically routes to the best matching agent:
```json
{
  "required_capability": "web_search",
  "input": "What is the capital of France?"
}
```

#### Real-Time SSE Streaming
```http
POST /a2a/v1/tasks/stream
Authorization: Bearer <API_KEY>
Content-Type: application/json

{
  "agent_id": "researcher",
  "input": "Stream research notes..."
}
```
**SSE Event Sequence**:
```text
event: task_started
data: {"agent_id":"researcher"}

event: token_delta
data: {"delta":"Here are the findings"}

event: task_completed
data: {"status":"COMPLETED"}
```

---

### 2. Unified REST Endpoints

For non-A2A clients that just want direct invocation:
- **Invoke**: `POST /api/v1/invoke/{agent_id}`
- **Stream**: `POST /api/v1/stream/{agent_id}`

---

### 3. Operational & Telemetry Endpoints

- **Liveness**: `GET /healthz`
- **Readiness**: `GET /readyz` (validates config & adapter registry)
- **Prometheus Metrics**: `GET /metrics`
  - `a2a_requests_total{method, path, status}`
  - `a2a_request_duration_seconds{method, path}`
  - `a2a_active_streams`
  - `a2a_retries_total{agent_id}`
  - `a2a_fallback_triggered_total{primary_agent, fallback_agent}`

---

## 📊 Benchmarks

Run on an Apple M2 (8-core ARM64) using Go 1.26:

| Component / Scenario | Latency / Op | Throughput | Allocations |
| :--- | :--- | :--- | :--- |
| **Direct Dispatcher (OpenAI)** | **47.4 µs** | ~21,000 req/s | 139 allocs/op (11.9 KB) |
| **Direct Dispatcher (LangGraph)** | **47.2 µs** | ~21,000 req/s | 126 allocs/op (11.2 KB) |
| **Direct Dispatcher (Custom Webhook)** | **48.9 µs** | ~20,400 req/s | 133 allocs/op (10.8 KB) |
| **Full HTTP Router (A2A Task Endpoint)**| **61.7 µs** | ~16,200 req/s | 193 allocs/op (20.8 KB) |
| **SSE Stream Connection Setup** | **95.9 µs** | ~10,400 streams/s | 361 allocs/op (104 KB) |
| **LangGraph Response Parsing** | **293.0 ns** | ~3,400,000 ops/s | 6 allocs/op (784 B) |
| **AutoGen Response Parsing** | **286.0 ns** | ~3,500,000 ops/s | 6 allocs/op (800 B) |
| **Custom Template Request Translation** | **2.18 µs** | ~460,000 ops/s | 29 allocs/op (2.0 KB) |

---

## 🛡️ Resilience & Production Hardening

- **Mid-Stream Guard**: Never cascades to fallback agents once tokens have been delivered to the client, preventing duplicate or corrupted SSE streams.
- **Thundering Herd Protection**: Exponential backoff with $\pm 20\%$ randomized jitter.
- **Static Cycle Detection**: DFS cycle validation catches circular fallback loops (`A -> B -> A`) at startup.
- **Anti-DoS Memory Limits**: Inbound request bodies are strictly capped at 10MB via `io.LimitReader`.
- **Timing Attack Resistance**: Inbound API keys are validated using constant-time comparison (`crypto/subtle.ConstantTimeCompare`).
- **Bounded Task Storage**: In-memory task response cache is bounded to 10,000 entries with FIFO eviction.

---

## 🧪 Testing

```bash
# Run unit & integration tests with race detector
make test

# Run performance benchmarks
make bench
```

---

## 🤝 Contributing

Contributions are welcome! Please check out [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on development, testing, and submitting pull requests.

---

## 📄 License

ProGoA2A is licensed under the [MIT License](LICENSE).
