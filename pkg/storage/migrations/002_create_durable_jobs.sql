-- Migration: 002_create_durable_jobs.sql
-- Idempotent schema definition for durable jobs ledger.
-- Delivery semantics are explicitly at-least-once.

CREATE TABLE IF NOT EXISTS durable_jobs (
    id TEXT NOT NULL CHECK (length(trim(id)) BETWEEN 1 AND 256),
    tenant_id TEXT NOT NULL CHECK (length(trim(tenant_id)) BETWEEN 1 AND 128),
    idempotency_key TEXT NOT NULL CHECK (length(trim(idempotency_key)) BETWEEN 1 AND 512),
    request_hash TEXT NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    request JSONB NOT NULL,
    agent_id TEXT CHECK (agent_id IS NULL OR length(trim(agent_id)) BETWEEN 1 AND 256),
    required_capability TEXT CHECK (required_capability IS NULL OR length(trim(required_capability)) BETWEEN 1 AND 256),
    state TEXT NOT NULL CHECK (state IN ('queued', 'leased', 'running', 'succeeded', 'failed', 'canceled', 'dead_letter')),
    attempt INT NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    max_attempts INT NOT NULL DEFAULT 1 CHECK (max_attempts >= 1 AND max_attempts <= 100),
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_owner TEXT CHECK (lease_owner IS NULL OR length(trim(lease_owner)) BETWEEN 1 AND 256),
    lease_token BIGINT NOT NULL DEFAULT 0 CHECK (lease_token >= 0),
    lease_expires_at TIMESTAMPTZ,
    response JSONB,
    failure_code TEXT CHECK (failure_code IS NULL OR length(failure_code) <= 128),
    failure_message TEXT CHECK (failure_message IS NULL OR length(failure_message) <= 2048),
    cancel_requested_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT pk_durable_jobs PRIMARY KEY (tenant_id, id),
    CONSTRAINT uq_durable_jobs_tenant_idempotency UNIQUE (tenant_id, idempotency_key),
    CONSTRAINT chk_durable_jobs_routing CHECK (
        (agent_id IS NOT NULL AND length(trim(agent_id)) > 0) OR
        (required_capability IS NOT NULL AND length(trim(required_capability)) > 0)
    ),
    CONSTRAINT chk_durable_jobs_lease_state CHECK (
        (state IN ('leased', 'running') AND lease_owner IS NOT NULL AND length(trim(lease_owner)) > 0 AND lease_token > 0 AND lease_expires_at IS NOT NULL) OR
        (state NOT IN ('leased', 'running') AND lease_owner IS NULL AND lease_expires_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_durable_jobs_tenant_lookup ON durable_jobs (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_durable_jobs_ready_to_lease ON durable_jobs (next_run_at, created_at) WHERE state = 'queued';
CREATE INDEX IF NOT EXISTS idx_durable_jobs_expired_leases ON durable_jobs (lease_expires_at) WHERE state IN ('leased', 'running');
