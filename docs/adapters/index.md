# Adapters

An adapter converts a `TaskRequest` into an upstream HTTP request and converts the upstream response or stream into ProGoA2A's normalized schema.

| Type | Expected upstream style | Guide |
|---|---|---|
| `langgraph` | LangGraph run endpoint and optional SSE modes | [LangGraph](langgraph.md) |
| `crewai` | Crew kickoff-style JSON endpoint | [CrewAI](crewai.md) |
| `autogen` | Sender/recipient/message JSON endpoint | [AutoGen](autogen.md) |
| `openai` | OpenAI-compatible Chat Completions endpoint | [OpenAI](openai.md) |
| `custom` | User-defined HTTP template and JSON extraction paths | [Custom webhooks](custom-webhooks.md) |

All adapters use the configured `endpoint`, add optional outbound bearer or custom-header authentication, honor the per-attempt timeout, and share the dispatcher's HTTP connection pool.

!!! warning "Compatibility is shape-based"
    Framework products expose several deployment APIs and versions. These adapters implement the exact payloads documented in their guides; they are not universal clients for every endpoint a framework may expose. Verify the translated shape against your upstream and use the custom adapter when it differs.
