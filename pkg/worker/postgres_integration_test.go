package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegration_WorkerProcessesPostgresJob(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("skipping integration test: POSTGRES_TEST_DSN is not set")
	}

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer setupCancel()
	repo, err := storage.NewPostgresJobRepository(setupCtx, dsn, storage.PostgresOptions{MaxConns: 4, MinConns: 1})
	if err != nil {
		t.Fatalf("create postgres job repository: %v", err)
	}
	cleanupPool, err := pgxpool.New(setupCtx, dsn)
	if err != nil {
		repo.Close()
		t.Fatalf("create cleanup pool: %v", err)
	}

	prefix := fmt.Sprintf("worker-integration-%d", time.Now().UnixNano())
	tenantID := prefix + "-tenant"
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = cleanupPool.Exec(cleanCtx, "DELETE FROM durable_jobs WHERE tenant_id = $1", tenantID)
		cleanupPool.Close()
		repo.Close()
	})

	if err := repo.Migrate(setupCtx); err != nil {
		t.Fatalf("migrate job repository: %v", err)
	}
	created, err := repo.CreateJob(setupCtx, model.CreateJobInput{
		ID:             prefix + "-job",
		TenantID:       tenantID,
		IdempotencyKey: prefix + "-key",
		Request:        map[string]any{"input": "integration"},
		AgentID:        "integration-agent",
		MaxAttempts:    1,
	})
	if err != nil {
		t.Fatalf("create durable job: %v", err)
	}

	cfg := config.WorkerConfig{
		WorkerID:                 prefix + "-worker",
		Concurrency:              1,
		BatchSize:                1,
		PollIntervalMilliseconds: 10,
		LeaseDurationSeconds:     4,
		RenewalIntervalSeconds:   1,
		RetryBackoffSeconds:      1,
		DrainTimeoutSeconds:      2,
	}
	executor := &fakeExec{execFunc: func(context.Context, *model.Job) (any, error) {
		return map[string]any{"ok": true}, nil
	}}
	runCtx, cancelRun := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRun()
	eng := NewEngine(cfg, repo, executor)
	eng.onJobCompleted = func(*model.Job) { cancelRun() }

	if err := eng.Run(runCtx); err != nil {
		t.Fatalf("run worker: %v", err)
	}
	loaded, err := repo.GetJob(context.Background(), tenantID, created.ID)
	if err != nil {
		t.Fatalf("load completed durable job: %v", err)
	}
	if loaded.State != model.JobStateSucceeded || loaded.Attempt != 1 {
		t.Fatalf("completed job state=%q attempt=%d, want succeeded/1", loaded.State, loaded.Attempt)
	}
	var response map[string]bool
	if err := json.Unmarshal(loaded.Response, &response); err != nil {
		t.Fatalf("decode persisted response: %v", err)
	}
	if !response["ok"] {
		t.Fatalf("unexpected persisted response: %s", loaded.Response)
	}
}
