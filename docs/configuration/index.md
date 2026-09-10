# Configuration

ProGoA2A loads one YAML file at startup. The CLI does not hot-reload it; restart the process after a change.

## Top-level sections

| Section | Purpose |
|---|---|
| `server` | Listen address and HTTP timeouts |
| `storage` | Pluggable task storage: in-memory FIFO cache or durable PostgreSQL |
| `security` | Inbound API keys and per-agent allowlists |
| `agents` | Upstream endpoints, adapter types, retry/fallback policy, auth, and adapter-specific options |

Start with the [complete field reference](reference.md), then review [Security & RBAC](security-rbac.md) and [PostgreSQL Storage](../deployment/postgresql.md) before exposing the service outside a trusted development network.

## Validation performed at startup

The loader applies defaults, then checks the listen port, unique/non-empty agent IDs, supported adapter types, absolute HTTP(S) endpoints, retry/timeout bounds, authentication settings, custom-adapter mappings, non-empty/unique inbound keys when security is enabled, allowed-agent references, fallback references, self-fallbacks, fallback cycles, and storage backend bounds (including mutual exclusion between memory and postgres settings).

## Secrets

`${ENV_VAR}` references are replaced before YAML parsing. Missing variables become empty strings. Empty inbound keys are rejected when security is enabled; adapter credentials are not universally required, so validate upstream secrets in your deployment process.
