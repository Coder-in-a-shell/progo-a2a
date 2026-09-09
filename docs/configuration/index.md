# Configuration

ProGoA2A loads one YAML file at startup. The CLI does not hot-reload it; restart the process after a change.

## Top-level sections

| Section | Purpose |
|---|---|
| `server` | Listen address and HTTP timeouts |
| `security` | Inbound API keys and per-agent allowlists |
| `agents` | Upstream endpoints, adapter types, retry/fallback policy, auth, and adapter-specific options |

Start with the [complete field reference](reference.md), then review [Security & RBAC](security-rbac.md) before exposing the service outside a trusted development network.

## Validation performed at startup

The loader applies defaults, then checks the listen port, unique/non-empty agent IDs, agent type and endpoint presence, custom mapping presence, non-empty/unique inbound keys when security is enabled, allowed-agent references, fallback references, self-fallbacks, and fallback cycles. Adapter names themselves are resolved when a request is dispatched, so use one of `langgraph`, `crewai`, `autogen`, `openai`, or `custom`.

## Secrets

`${ENV_VAR}` references are replaced before YAML parsing. Missing variables become empty strings. Empty inbound keys are rejected when security is enabled; adapter credentials are not universally required, so validate upstream secrets in your deployment process.
