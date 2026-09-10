-- Migration: 001_create_task_results.sql
-- Idempotent schema definition for task results storage.

CREATE TABLE IF NOT EXISTS task_results (
    lookup_id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    status TEXT NOT NULL,
    response JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_task_results_task_id ON task_results (task_id);
CREATE INDEX IF NOT EXISTS idx_task_results_updated_at ON task_results (updated_at);
