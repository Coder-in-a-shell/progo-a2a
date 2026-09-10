# PostgreSQL Task Storage

ProGoA2A provides pluggable task storage with two backends: the default in-memory cache and a durable PostgreSQL backend.

---

## Storage Backends: Memory vs PostgreSQL

| Feature / Behavior | In-Memory (`memory`) | PostgreSQL (`postgres`) |
|---|---|---|
| **Primary Use Case** | Local development, testing, single-instance deployments | Multi-replica production clusters, durable task lookup |
| **Durability** | Ephemeral (cleared on process restart) | Durable (CI-tested with PostgreSQL 17) |
| **Horizontal Sharing** | Process-local; not shared across pods/replicas | Shared across all proxy replicas connecting to the database |
| **External Dependencies** | None (zero external configuration required) | PostgreSQL cluster |
| **Capacity & Retention** | Bounded FIFO ring (default 10,000 tasks, configurable up to 1,000,000) | Unbounded table; managed via operator retention policies |
| **Readiness Probe (`/readyz`)** | Verifies configuration and adapter registration | Verifies database connectivity with a 2-second ping timeout |
| **Lookup Path** | Process-local map lookup | Indexed SQL query over a connection pool |

### When to Use Which

- **Use `memory`** when running locally, in CI testing without a database, in staging environments where task retrieval across restarts is unnecessary, or in production deployments where clients only consume the immediate synchronous response and never call `GET /a2a/v1/tasks/{task_id}`.
- **Use `postgres`** when running multiple horizontal replicas behind a load balancer and clients retrieve completed task results via `GET /a2a/v1/tasks/{task_id}`, or when completed task results must survive proxy container restarts.

### Protocol and Storage Boundaries

Be precise about what PostgreSQL storage does and does not do:

- **Durable lookup & background jobs**: PostgreSQL provides durable storage for completed synchronous task responses (`task_results`) as well as a durable ledger (`durable_jobs`) for background worker execution with at-least-once delivery semantics.
- **Worker runtime**: When running in `worker` or `all` roles, background workers consume and process durable jobs using distributed leasing, scheduled renewal, and optimistic fencing. Public async HTTP endpoints for submitting durable jobs are planned for a subsequent release.
- **Cancellation boundary**: The repository and worker support fenced cancellation acknowledgement internally. A public HTTP cancellation endpoint is not available in this release.
- **No streaming event persistence**: Events streamed over Server-Sent Events (`/tasks/stream` or `/api/v1/stream/{agent_id}`) are delivered directly to the connected client and are **not** persisted to the database.
- **At-least-once delivery**: The worker runtime provides at-least-once execution; jobs may be reclaimed and retried after lease expiration, so downstream operations must be idempotent when retries are enabled.
- **No automatic retention**: The database schema does not automatically expire or prune historical task results.
- **Not official A2A 1.0**: The storage model persists ProGoA2A's internal task response structure; it does not implement the official A2A JSON-RPC specification. See the [A2A protocol scope](../concepts/a2a-protocol.md) for precise boundaries.

---

## Configuration Schema, Defaults, and Bounds

Storage is configured under the top-level `storage` key in `progo-a2a.yaml` (see the [configuration reference](../configuration/reference.md) for all server settings):

```yaml
storage:
  backend: "postgres" # "memory" | "postgres" (default: "memory")

  # In-memory settings (valid ONLY when backend is "memory")
  memory:
    max_tasks: 10000 # integer, 0 uses the 10,000 default; maximum 1,000,000

  # PostgreSQL settings (valid ONLY when backend is "postgres")
  postgres:
    dsn: "${DATABASE_URL}" # string, required when backend is postgres
    max_connections: 20 # integer, bounds: 1 to 1000 (default: 20)
    min_connections: 2 # integer, bounds: 0 to max_connections (default: 2)
    max_connection_lifetime_seconds: 1800 # integer, > 0 (default: 1800 = 30 min)
    max_connection_idle_time_seconds: 300 # integer, > 0 (default: 300 = 5 min)
    health_check_period_seconds: 30 # integer, > 0 (default: 30)
    connect_timeout_seconds: 5 # integer, > 0 (default: 5)
    migrate_on_start: false # boolean (default: false)
```

### Validation and Mutual Exclusion Rules

The configuration validator enforces strict mutual exclusion between backends:

- When `backend: memory` (or unset), no `postgres` settings (including `dsn`) may be configured.
- When `backend: postgres`, no `memory` settings may be configured, and `postgres.dsn` must be non-empty and not solely whitespace.
- Connection pool bounds are validated statically at startup: `min_connections` cannot exceed `max_connections`, and all lifetime and timeout durations must be strictly positive integers.

---

## Environment-Based Dual-Mode Configuration

To support zero-config in-memory mode for local development while enabling seamless PostgreSQL activation in containers or Kubernetes, `config/progo-a2a.example.yaml` uses environment variable expansion:

```yaml
storage:
  backend: ${STORAGE_BACKEND}
  postgres:
    dsn: ${DATABASE_URL}
    migrate_on_start: ${MIGRATE_ON_START}
```

- When `STORAGE_BACKEND` is unset, the loader defaults to `memory`, leaves PostgreSQL settings empty, and applies default in-memory bounds (`max_tasks: 10000`).
- When `STORAGE_BACKEND=postgres`, `DATABASE_URL` provides the connection DSN, `MIGRATE_ON_START` controls schema migration, and the loader supplies default pool settings.

---

## Docker Compose Quickstart

The default `docker-compose.yml` runs ProGoA2A alongside a PostgreSQL 17 Alpine container (see the [Docker & Compose guide](docker.md) for image details):

```bash
# Start the stack (gateway + PostgreSQL)
docker compose up -d --build
# Or use the Makefile shortcut:
make docker-up

# Verify health and readiness
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/readyz

# Inspect logs
docker compose logs -f progo-a2a

# Stop the stack safely (preserves the named database volume postgres_data)
docker compose down
# Or use the Makefile shortcut:
make docker-down
```

!!! warning "Local Development Defaults Only"
    The default passwords and API keys in `docker-compose.yml` and `.env.example` are strictly for local development. Overwrite `POSTGRES_PASSWORD` and all API keys via host environment variables or a `.env` file before exposing the services. By default, the PostgreSQL container port (`5432`) is not published to the host network.

---

## Database Migrations

### Embedded Idempotent Schema

ProGoA2A embeds its database migrations into the application binary. The schema defines the `task_results` table:

```sql
CREATE TABLE IF NOT EXISTS task_results (
    lookup_id   TEXT PRIMARY KEY,
    task_id     TEXT NOT NULL,
    agent_id    TEXT NOT NULL,
    status      TEXT NOT NULL,
    response    JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_task_results_task_id ON task_results (task_id);
CREATE INDEX IF NOT EXISTS idx_task_results_updated_at ON task_results (updated_at);
```

### Advisory Lock Concurrency Control

When migrations run, ProGoA2A acquires a transaction-scoped PostgreSQL advisory lock (`0x5461736b53746f72`, the ASCII representation of `"TaskStor"`). If multiple proxy instances start concurrently with `migrate_on_start: true`, the advisory lock serializes migration execution, preventing race conditions or duplicate DDL errors.

### Production Migration Recommendations

- `migrate_on_start` is `false` by default.
- While `migrate_on_start: true` is convenient for local development and Docker Compose, **multi-replica production deployments should keep `migrate_on_start: false`**.
- Apply the checked-in `pkg/storage/migrations/001_create_task_results.sql` once from a serialized CI/CD migration step before rolling out replicas. The SQL is idempotent, but direct execution does not acquire the application's advisory lock, so do not run multiple manual migration jobs concurrently.
- Alternatively, start one controlled instance with `migrate_on_start: true`, wait for it to become ready, then deploy the remaining replicas with `migrate_on_start: false`.

---

## Table Schema and Storage Mechanics

### Canonical IDs and Aliases

When a task executes, it has a canonical `task_id` (generated by the proxy or downstream adapter) and may also have a client-specified request `ID` (alias).

To provide O(1) lookup regardless of which identifier the client presents:

1. The response JSON is marshaled once in memory.
2. Canonical ID and alias IDs are deduplicated into a list of unique lookup keys.
3. All keys are inserted atomically in a single SQL statement using PostgreSQL's `unnest()`:

```sql
INSERT INTO task_results (lookup_id, task_id, agent_id, status, response, created_at, updated_at)
SELECT
    k,
    $2,
    $3,
    $4,
    $5::jsonb,
    NOW(),
    NOW()
FROM unnest($1::text[]) AS k
ON CONFLICT (lookup_id) DO UPDATE SET
    task_id    = EXCLUDED.task_id,
    agent_id   = EXCLUDED.agent_id,
    status     = EXCLUDED.status,
    response   = EXCLUDED.response,
    updated_at = NOW();
```

### Preservation of `created_at`

On conflict (such as when an existing task is updated with a final status), the atomic upsert updates `task_id`, `agent_id`, `status`, `response`, and `updated_at`, while preserving the initial `created_at` timestamp.

---

## Readiness and Failure Semantics

### Readiness Probe (`/readyz`)

When PostgreSQL storage is enabled, the `/readyz` endpoint tests database connectivity:

- Executes `Ping(ctx)` on the connection pool with a **2-second timeout**.
- If the database is unreachable or fails the ping, `/readyz` responds with HTTP `503 Service Unavailable`:
  ```json
  {
    "status": "not ready",
    "error": "storage unavailable",
    "adapters": 5,
    "time": "2026-09-10T12:00:00Z"
  }
  ```
- Kubernetes readiness probes will automatically mark the pod unready, preventing incoming traffic from routing to a replica that cannot reach storage.

### Downstream Success with Storage Failure (HTTP 503)

If an upstream agent completes successfully but the database operation fails during `Save()` (for example, due to a database network partition or query timeout):

1. The proxy logs the storage persistence failure with structured error details (credentials in the error message are automatically sanitized and redacted).
2. The proxy returns an HTTP `503 Service Unavailable` with error code `TASK_STORAGE_UNAVAILABLE`:
  ```json
  {
    "error": {
      "code": "TASK_STORAGE_UNAVAILABLE",
      "message": "task storage is temporarily unavailable",
      "agent_id": "langgraph-researcher",
      "status": 503,
      "timestamp": "2026-09-10T12:00:00Z"
    }
  }
  ```
3. The `503` tells callers that the result was not durably recorded. The downstream agent may already have completed, so clients must not blindly retry unless that operation is idempotent; retain the request/task identifier and reconcile with the downstream system when possible.

---

## Operations, Security, and Maintenance

Before deploying to production, review the [production checklist](production-checklist.md) and [Kubernetes deployment guide](kubernetes.md).

### Connection Pool Sizing

The PostgreSQL task store utilizes `pgxpool.Pool`. Tune pool settings according to your database cluster capacity:

- **Calculate Total Cluster Connections**: Total Connections = Replicas x `max_connections`. Ensure this does not exceed your PostgreSQL server's `max_connections` limit.
- **`min_connections`**: Pre-warms connections to reduce cold-start latency on traffic spikes.
- **`max_connection_lifetime_seconds`**: Periodically recycles connections to facilitate server-side load balancing and DNS updates.
- **`max_connection_idle_time_seconds`**: Closes idle connections above `min_connections` during low-traffic windows.

### Secret Management and TLS

- **Never commit credentials**: Pass `DATABASE_URL` using environment variables or container secret injection (e.g., Kubernetes Secrets, HashiCorp Vault, AWS Secrets Manager).
- **Enforce TLS**: In production environments, always append `sslmode=require`, `sslmode=verify-ca`, or `sslmode=verify-full` to the DSN to encrypt database traffic in transit.

### Unbounded Table Warning and Retention Responsibility

!!! warning "Operator Responsibility: Data Retention"
    The `task_results` table does not automatically purge old records. Over time, high-throughput systems will cause the table to grow continuously. Operators must implement a periodic retention cleanup policy (such as a cron job, pg_cron, or Kubernetes CronJob) based on organizational compliance requirements:

    ```sql
    -- Example retention cleanup: purge results older than 30 days
    DELETE FROM task_results
    WHERE updated_at < NOW() - INTERVAL '30 days';
    ```

### Backup and Disaster Recovery

The task store relies entirely on standard PostgreSQL tables. Operators are responsible for configuring routine automated backups, write-ahead log (WAL) archiving, and point-in-time recovery (PITR) procedures.

### Rollback Guidance

Schema migrations are strictly idempotent and non-destructive:

- Rolling back proxy application versions does not affect the `task_results` table.
- If switching the configuration backend back from `postgres` to `memory`, existing records remain safely stored in PostgreSQL; however, subsequent task dispatches and lookups will operate strictly against the ephemeral in-memory cache.

---

## Integration Testing

### CI Verification

The project CI pipeline automatically spins up a `postgres:17-alpine` service container and runs race-enabled tests against it by configuring `POSTGRES_TEST_DSN`.

### Running Integration Tests Locally

To run the full suite including PostgreSQL integration tests on your workstation:

```bash
# 1. Start a disposable PostgreSQL instance bound only to loopback
docker run --rm -d --name progo-a2a-postgres-test \
  -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres_test_password \
  -e POSTGRES_DB=progo_test \
  -p 127.0.0.1:5432:5432 postgres:17-alpine

# 2. Run tests with POSTGRES_TEST_DSN set
POSTGRES_TEST_DSN="postgres://postgres:postgres_test_password@localhost:5432/progo_test?sslmode=disable" go test -v -race ./...

# 3. Or run only storage integration tests:
POSTGRES_TEST_DSN="postgres://postgres:postgres_test_password@localhost:5432/progo_test?sslmode=disable" go test -v -race ./pkg/storage/...

# 4. Stop the disposable test database
docker stop progo-a2a-postgres-test
```

If `POSTGRES_TEST_DSN` is not provided, integration tests gracefully skip, allowing unit tests to run in disconnected environments.
