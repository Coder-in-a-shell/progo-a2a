package storage

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

func setupJobsIntegrationTest(t *testing.T) (*PostgresJobRepository, string) {
	t.Helper()
	dsn := getTestDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	repo, err := NewPostgresJobRepository(ctx, dsn, PostgresOptions{
		MaxConns: 10,
		MinConns: 2,
	})
	if err != nil {
		t.Fatalf("failed to create test postgres job repository: %v", err)
	}

	if err := repo.Migrate(ctx); err != nil {
		repo.Close()
		t.Fatalf("failed to migrate test database: %v", err)
	}

	prefix := fmt.Sprintf("test-job-%s-%d-%d", t.Name(), time.Now().UnixNano(), rand.Intn(100000))

	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanCancel()

		if repo.pool != nil {
			_, _ = repo.pool.Exec(cleanCtx, "DELETE FROM durable_jobs WHERE tenant_id LIKE $1 OR id LIKE $1", prefix+"%")
		}
		repo.Close()
	})

	return repo, prefix
}

func mustAcquireOne(t *testing.T, repo *PostgresJobRepository, owner string) *model.Job {
	t.Helper()
	jobs, err := repo.AcquireLeases(context.Background(), owner, 1, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireLeases failed: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("AcquireLeases returned %d jobs, want 1", len(jobs))
	}
	return jobs[0]
}

func mustGetJob(t *testing.T, repo *PostgresJobRepository, tenantID, jobID string) *model.Job {
	t.Helper()
	job, err := repo.GetJob(context.Background(), tenantID, jobID)
	if err != nil {
		t.Fatalf("GetJob(%q, %q) failed: %v", tenantID, jobID, err)
	}
	return job
}

func TestIntegration_JobsMigration_AdoptionConcurrencyAndChecksum(t *testing.T) {
	repo, _ := setupJobsIntegrationTest(t)
	ctx := context.Background()

	// 1. Repeated migration idempotency
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("repeated migration failed: %v", err)
	}

	// 2. Concurrent migrations with advisory lock
	var wg sync.WaitGroup
	errCh := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := repo.Migrate(ctx); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent migration failed: %v", err)
	}

	// 3. Checksum mismatch detection
	var origName, origChecksum string
	err := repo.pool.QueryRow(ctx, "SELECT name, checksum FROM schema_migrations WHERE version = 1").Scan(&origName, &origChecksum)
	if err != nil {
		t.Fatalf("failed to query version 1 checksum: %v", err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(context.Background(), "DELETE FROM schema_migrations WHERE version = 999")
		_, _ = repo.pool.Exec(context.Background(), "UPDATE schema_migrations SET name = $1, checksum = $2 WHERE version = 1", origName, origChecksum)
	})

	// Tamper with checksum for version 1
	tamperedChecksum := strings.Repeat("0", 64)
	_, err = repo.pool.Exec(ctx, "UPDATE schema_migrations SET checksum = $1 WHERE version = 1", tamperedChecksum)
	if err != nil {
		t.Fatalf("failed to tamper checksum: %v", err)
	}

	// Must fail with ErrMigrationChecksumMismatch
	err = repo.Migrate(ctx)
	if err == nil {
		t.Fatal("expected migration checksum mismatch error, got nil")
	}
	if !errors.Is(err, ErrMigrationChecksumMismatch) {
		t.Fatalf("expected ErrMigrationChecksumMismatch, got: %v", err)
	}

	// Restore original checksum
	_, err = repo.pool.Exec(ctx, "UPDATE schema_migrations SET checksum = $1 WHERE version = 1", origChecksum)
	if err != nil {
		t.Fatalf("failed to restore checksum: %v", err)
	}

	// Re-verifying passes after restore
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migration after checksum restore failed: %v", err)
	}

	// 4. Recorded filename mismatch detection
	if _, err := repo.pool.Exec(ctx, "UPDATE schema_migrations SET name = '001_renamed.sql' WHERE version = 1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(ctx); !errors.Is(err, ErrMigrationNameMismatch) {
		t.Fatalf("expected ErrMigrationNameMismatch, got %v", err)
	}
	if _, err := repo.pool.Exec(ctx, "UPDATE schema_migrations SET name = $1 WHERE version = 1", origName); err != nil {
		t.Fatal(err)
	}

	// 5. Refuse to run an older binary against a newer schema.
	if _, err := repo.pool.Exec(ctx, `INSERT INTO schema_migrations (version, name, checksum) VALUES (999, '999_future.sql', $1)`, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(ctx); !errors.Is(err, ErrMigrationVersionTooNew) {
		t.Fatalf("expected ErrMigrationVersionTooNew, got %v", err)
	}
	if _, err := repo.pool.Exec(ctx, "DELETE FROM schema_migrations WHERE version = 999"); err != nil {
		t.Fatal(err)
	}
}

func TestIntegration_Jobs_IdempotencyAndConflict(t *testing.T) {
	repo, prefix := setupJobsIntegrationTest(t)
	ctx := context.Background()

	tenant := prefix + "-tenant"
	idempotencyKey := prefix + "-key-1"
	jobID1 := prefix + "-job-1"
	jobID2 := prefix + "-job-2"

	req1 := map[string]any{"task": "generate_report", "format": "pdf"}
	input1 := model.CreateJobInput{
		ID:             jobID1,
		TenantID:       tenant,
		IdempotencyKey: idempotencyKey,
		AgentID:        "agent-writer",
		MaxAttempts:    3,
		Request:        req1,
	}

	// First creation succeeds
	created1, err := repo.CreateJob(ctx, input1)
	if err != nil {
		t.Fatalf("first CreateJob failed: %v", err)
	}
	if created1.ID != jobID1 || created1.State != model.JobStateQueued {
		t.Fatalf("unexpected created job: %+v", created1)
	}

	// Same tenant, same key, same request: idempotent success returning existing job
	inputSame := model.CreateJobInput{
		ID:             jobID2, // Different job ID in input, but same tenant & key & request
		TenantID:       tenant,
		IdempotencyKey: idempotencyKey,
		AgentID:        "agent-writer",
		MaxAttempts:    3,
		Request:        req1,
	}
	createdSame, err := repo.CreateJob(ctx, inputSame)
	if err != nil {
		t.Fatalf("idempotent CreateJob failed: %v", err)
	}
	if createdSame.ID != jobID1 {
		t.Fatalf("expected existing job ID %s, got %s", jobID1, createdSame.ID)
	}

	// Same tenant, same key, DIFFERENT request: must return ErrIdempotencyConflict
	reqDiff := map[string]any{"task": "generate_report", "format": "csv"}
	inputDiff := model.CreateJobInput{
		ID:             prefix + "-job-3",
		TenantID:       tenant,
		IdempotencyKey: idempotencyKey,
		AgentID:        "agent-writer",
		MaxAttempts:    3,
		Request:        reqDiff,
	}
	_, err = repo.CreateJob(ctx, inputDiff)
	if err == nil {
		t.Fatal("expected ErrIdempotencyConflict for different request, got nil")
	}
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got: %v", err)
	}
}

func TestIntegration_Jobs_TenantIsolation(t *testing.T) {
	repo, prefix := setupJobsIntegrationTest(t)
	ctx := context.Background()

	tenantA := prefix + "-tenant-a"
	tenantB := prefix + "-tenant-b"
	sharedKey := prefix + "-shared-key"

	inputA := model.CreateJobInput{
		ID:             prefix + "-job-a",
		TenantID:       tenantA,
		IdempotencyKey: sharedKey,
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        map[string]any{"user": "alice"},
	}
	inputB := model.CreateJobInput{
		ID:             prefix + "-job-b",
		TenantID:       tenantB,
		IdempotencyKey: sharedKey,
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        map[string]any{"user": "bob"},
	}

	// Both tenants create jobs with the same idempotency key without conflict
	jobA, err := repo.CreateJob(ctx, inputA)
	if err != nil {
		t.Fatalf("tenant A create failed: %v", err)
	}
	jobB, err := repo.CreateJob(ctx, inputB)
	if err != nil {
		t.Fatalf("tenant B create failed: %v", err)
	}

	if jobA.ID == jobB.ID {
		t.Fatalf("tenant jobs must have distinct IDs: %s == %s", jobA.ID, jobB.ID)
	}

	// Cross-tenant get must fail with ErrJobNotFound
	_, err = repo.GetJob(ctx, tenantB, jobA.ID)
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("expected ErrJobNotFound for cross-tenant access, got: %v", err)
	}

	// Same tenant get succeeds
	loadedA, err := repo.GetJob(ctx, tenantA, jobA.ID)
	if err != nil {
		t.Fatalf("tenant A get failed: %v", err)
	}
	if loadedA.ID != jobA.ID {
		t.Fatalf("loaded ID mismatch: %s != %s", loadedA.ID, jobA.ID)
	}
}

func TestIntegration_Jobs_ConcurrentBatchLeasing(t *testing.T) {
	repo, prefix := setupJobsIntegrationTest(t)
	ctx := context.Background()

	const numJobs = 20
	const numWorkers = 5

	for i := 0; i < numJobs; i++ {
		input := model.CreateJobInput{
			ID:             fmt.Sprintf("%s-lease-job-%02d", prefix, i),
			TenantID:       prefix + "-tenant",
			IdempotencyKey: fmt.Sprintf("%s-lease-key-%02d", prefix, i),
			AgentID:        "agent-worker",
			MaxAttempts:    3,
			Request:        map[string]any{"index": i},
		}
		if _, err := repo.CreateJob(ctx, input); err != nil {
			t.Fatalf("failed to create job %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	leasedJobIDs := make(map[string]string) // jobID -> workerID
	errCh := make(chan error, numWorkers)

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		workerID := fmt.Sprintf("worker-%s-%d", prefix, w)
		go func(wid string) {
			defer wg.Done()
			jobs, err := repo.AcquireLeases(ctx, wid, 10, 30*time.Second)
			if err != nil {
				errCh <- err
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, j := range jobs {
				if existingWorker, dup := leasedJobIDs[j.ID]; dup {
					errCh <- fmt.Errorf("duplicate lease on %s: held by %s and claimed by %s", j.ID, existingWorker, wid)
					return
				}
				leasedJobIDs[j.ID] = wid
				if j.State != model.JobStateLeased {
					errCh <- fmt.Errorf("job %s state is %s, expected leased", j.ID, j.State)
					return
				}
				if j.Attempt != 1 {
					errCh <- fmt.Errorf("job %s attempt is %d, expected 1", j.ID, j.Attempt)
					return
				}
				if j.LeaseToken != 1 {
					errCh <- fmt.Errorf("job %s lease_token is %d, expected 1", j.ID, j.LeaseToken)
					return
				}
			}
		}(workerID)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}

	if len(leasedJobIDs) != numJobs {
		t.Fatalf("expected all %d jobs leased, got %d", numJobs, len(leasedJobIDs))
	}
}

func TestIntegration_Jobs_DeterministicOrderingAndFutureExclusion(t *testing.T) {
	repo, prefix := setupJobsIntegrationTest(t)
	ctx := context.Background()

	past1 := time.Now().Add(-10 * time.Minute)
	past2 := time.Now().Add(-5 * time.Minute)
	future := time.Now().Add(1 * time.Hour)

	job1 := model.CreateJobInput{
		ID:             prefix + "-past-1",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-k1",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        "past-1",
		NextRunAt:      &past1,
	}
	job2 := model.CreateJobInput{
		ID:             prefix + "-past-2",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-k2",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        "past-2",
		NextRunAt:      &past2,
	}
	jobFuture := model.CreateJobInput{
		ID:             prefix + "-future",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-k3",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        "future",
		NextRunAt:      &future,
	}

	if _, err := repo.CreateJob(ctx, job2); err != nil { // inserted first but past2 > past1
		t.Fatal(err)
	}
	if _, err := repo.CreateJob(ctx, job1); err != nil { // inserted second but past1 < past2
		t.Fatal(err)
	}
	if _, err := repo.CreateJob(ctx, jobFuture); err != nil {
		t.Fatal(err)
	}

	// Acquire up to 10 jobs
	leased, err := repo.AcquireLeases(ctx, "worker-test", 10, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// Filter down to this test's prefix
	var myLeased []*model.Job
	for _, j := range leased {
		if strings.HasPrefix(j.ID, prefix) {
			myLeased = append(myLeased, j)
		}
	}

	if len(myLeased) != 2 {
		t.Fatalf("expected exactly 2 past jobs leased, got %d", len(myLeased))
	}
	if myLeased[0].ID != job1.ID {
		t.Fatalf("expected first job to be %s (earlier next_run_at), got %s", job1.ID, myLeased[0].ID)
	}
	if myLeased[1].ID != job2.ID {
		t.Fatalf("expected second job to be %s, got %s", job2.ID, myLeased[1].ID)
	}
}

func TestIntegration_Jobs_FencingAndLeaseLost(t *testing.T) {
	repo, prefix := setupJobsIntegrationTest(t)
	ctx := context.Background()

	input := model.CreateJobInput{
		ID:             prefix + "-fence-job",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-fence-key",
		AgentID:        "agent-fenced",
		MaxAttempts:    3,
		Request:        "payload",
	}
	if _, err := repo.CreateJob(ctx, input); err != nil {
		t.Fatal(err)
	}

	leased, err := repo.AcquireLeases(ctx, "worker-correct", 1, 30*time.Second)
	if err != nil || len(leased) != 1 {
		t.Fatalf("failed to acquire lease: %v, len=%d", err, len(leased))
	}
	job := leased[0]

	// 1. Wrong owner fence failure
	wrongOwnerFence := job.Fence()
	wrongOwnerFence.LeaseOwner = "worker-wrong"
	err = repo.MarkRunning(ctx, wrongOwnerFence)
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost for wrong owner, got: %v", err)
	}

	// 2. Wrong token fence failure
	wrongTokenFence := job.Fence()
	wrongTokenFence.LeaseToken = 999
	err = repo.MarkRunning(ctx, wrongTokenFence)
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost for wrong token, got: %v", err)
	}

	// 3. Correct fence mark running succeeds
	if err := repo.MarkRunning(ctx, job.Fence()); err != nil {
		t.Fatalf("expected MarkRunning to succeed, got: %v", err)
	}

	// 4. MarkRunning again on running job fails with ErrLeaseLost (wrong state: already running)
	err = repo.MarkRunning(ctx, job.Fence())
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost for MarkRunning on already running job, got: %v", err)
	}

	// 5. Expired lease fence failure: artificially expire lease in DB
	_, err = repo.pool.Exec(ctx, "UPDATE durable_jobs SET lease_expires_at = NOW() - INTERVAL '5 seconds' WHERE id = $1", job.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Mutations on expired lease must return ErrLeaseLost
	if err := repo.RenewLease(ctx, job.Fence(), 30*time.Second); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost on expired RenewLease, got: %v", err)
	}
	if err := repo.CompleteJob(ctx, job.Fence(), "res"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost on expired CompleteJob, got: %v", err)
	}
	failInput := model.FailJobInput{
		TenantID:   job.TenantID,
		JobID:      job.ID,
		LeaseOwner: *job.LeaseOwner,
		LeaseToken: job.LeaseToken,
	}
	if err := repo.FailJob(ctx, failInput); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost on expired FailJob, got: %v", err)
	}
}

func TestIntegration_Jobs_LifecycleTransitions(t *testing.T) {
	repo, prefix := setupJobsIntegrationTest(t)
	ctx := context.Background()

	// 1. Success transition
	succInput := model.CreateJobInput{
		ID:             prefix + "-succ",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-succ-key",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        "req",
	}
	if _, err := repo.CreateJob(ctx, succInput); err != nil {
		t.Fatal(err)
	}
	succJob := mustAcquireOne(t, repo, "worker-1")
	if err := repo.MarkRunning(ctx, succJob.Fence()); err != nil {
		t.Fatal(err)
	}
	respData := map[string]any{"result": "success", "count": 100}
	if err := repo.CompleteJob(ctx, succJob.Fence(), respData); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.GetJob(ctx, succJob.TenantID, succJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != model.JobStateSucceeded || loaded.LeaseOwner != nil || loaded.LeaseExpiresAt != nil {
		t.Fatalf("invalid succeeded job state: %+v", loaded)
	}

	// 2. Retryable failure returns to queued when attempts remain
	retryInput := model.CreateJobInput{
		ID:             prefix + "-retry",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-retry-key",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        "req",
	}
	if _, err := repo.CreateJob(ctx, retryInput); err != nil {
		t.Fatal(err)
	}
	leasedRetry := mustAcquireOne(t, repo, "worker-1")
	failRetryInput := model.FailJobInput{
		TenantID:       leasedRetry.TenantID,
		JobID:          leasedRetry.ID,
		LeaseOwner:     *leasedRetry.LeaseOwner,
		LeaseToken:     leasedRetry.LeaseToken,
		FailureCode:    "TEMP_ERR",
		FailureMessage: "network timeout",
		Retryable:      true,
		Backoff:        time.Hour,
	}
	if err := repo.FailJob(ctx, failRetryInput); err != nil {
		t.Fatal(err)
	}
	loadedRetry := mustGetJob(t, repo, leasedRetry.TenantID, leasedRetry.ID)
	if loadedRetry.State != model.JobStateQueued || loadedRetry.Attempt != 1 || loadedRetry.LeaseOwner != nil {
		t.Fatalf("expected job to return to queued on retry: %+v", loadedRetry)
	}

	// 3. Non-retryable failure transitions to failed
	terminalFailInput := model.CreateJobInput{
		ID:             prefix + "-term-fail",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-term-key",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        "req",
	}
	if _, err := repo.CreateJob(ctx, terminalFailInput); err != nil {
		t.Fatal(err)
	}
	leasedTerm := mustAcquireOne(t, repo, "worker-1")
	failTermInput := model.FailJobInput{
		TenantID:       leasedTerm.TenantID,
		JobID:          leasedTerm.ID,
		LeaseOwner:     *leasedTerm.LeaseOwner,
		LeaseToken:     leasedTerm.LeaseToken,
		FailureCode:    "PERM_ERR",
		FailureMessage: "invalid syntax",
		Retryable:      false,
	}
	if err := repo.FailJob(ctx, failTermInput); err != nil {
		t.Fatal(err)
	}
	loadedTerm := mustGetJob(t, repo, leasedTerm.TenantID, leasedTerm.ID)
	if loadedTerm.State != model.JobStateFailed || loadedTerm.LeaseOwner != nil {
		t.Fatalf("expected job to transition to failed: %+v", loadedTerm)
	}

	// 4. Exhausted attempts transitions to dead_letter
	deadLetterInput := model.CreateJobInput{
		ID:             prefix + "-dead-letter",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-dead-letter-key",
		AgentID:        "agent-1",
		MaxAttempts:    1, // Only 1 attempt allowed
		Request:        "req",
	}
	if _, err := repo.CreateJob(ctx, deadLetterInput); err != nil {
		t.Fatal(err)
	}
	leasedDead := mustAcquireOne(t, repo, "worker-1")
	failDeadInput := model.FailJobInput{
		TenantID:       leasedDead.TenantID,
		JobID:          leasedDead.ID,
		LeaseOwner:     *leasedDead.LeaseOwner,
		LeaseToken:     leasedDead.LeaseToken,
		FailureCode:    "FATAL",
		FailureMessage: "out of retries",
		Retryable:      true, // retryable but attempts exhausted
	}
	if err := repo.FailJob(ctx, failDeadInput); err != nil {
		t.Fatal(err)
	}
	loadedDead := mustGetJob(t, repo, leasedDead.TenantID, leasedDead.ID)
	if loadedDead.State != model.JobStateDeadLetter || loadedDead.LeaseOwner != nil {
		t.Fatalf("expected job to transition to dead_letter: %+v", loadedDead)
	}

	// 5. Cancellation
	// Queued job cancels immediately
	cancelQueuedInput := model.CreateJobInput{
		ID:             prefix + "-cancel-queued",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-cancel-q-key",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        "req",
	}
	if _, err := repo.CreateJob(ctx, cancelQueuedInput); err != nil {
		t.Fatal(err)
	}
	if err := repo.CancelJob(ctx, cancelQueuedInput.TenantID, cancelQueuedInput.ID); err != nil {
		t.Fatal(err)
	}
	loadedCanceled := mustGetJob(t, repo, cancelQueuedInput.TenantID, cancelQueuedInput.ID)
	if loadedCanceled.State != model.JobStateCanceled {
		t.Fatalf("expected queued job to cancel immediately: %+v", loadedCanceled)
	}

	// Running job retains running state but sets cancel_requested_at
	cancelRunningInput := model.CreateJobInput{
		ID:             prefix + "-cancel-running",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-cancel-r-key",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        "req",
	}
	if _, err := repo.CreateJob(ctx, cancelRunningInput); err != nil {
		t.Fatal(err)
	}
	leasedRunning := mustAcquireOne(t, repo, "worker-1")
	if err := repo.MarkRunning(ctx, leasedRunning.Fence()); err != nil {
		t.Fatal(err)
	}
	if err := repo.CancelJob(ctx, cancelRunningInput.TenantID, cancelRunningInput.ID); err != nil {
		t.Fatal(err)
	}
	loadedRunning := mustGetJob(t, repo, cancelRunningInput.TenantID, cancelRunningInput.ID)
	if loadedRunning.State != model.JobStateRunning || loadedRunning.CancelRequestedAt == nil {
		t.Fatalf("expected running job to remain running with cancel_requested_at set: %+v", loadedRunning)
	}
}

func TestIntegration_Jobs_ExpiredLeaseReclamation(t *testing.T) {
	repo, prefix := setupJobsIntegrationTest(t)
	ctx := context.Background()

	// 1. Expired retryable job returns to queued
	job1Input := model.CreateJobInput{
		ID:             prefix + "-reclaim-retry",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-r1",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        "r1",
	}
	if _, err := repo.CreateJob(ctx, job1Input); err != nil {
		t.Fatal(err)
	}
	leased1 := mustAcquireOne(t, repo, "worker-1")
	if err := repo.MarkRunning(ctx, leased1.Fence()); err != nil {
		t.Fatal(err)
	}

	// 2. Expired cancellation-requested job becomes canceled
	job2Input := model.CreateJobInput{
		ID:             prefix + "-reclaim-cancel",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-r2",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        "r2",
	}
	if _, err := repo.CreateJob(ctx, job2Input); err != nil {
		t.Fatal(err)
	}
	_ = mustAcquireOne(t, repo, "worker-1")
	if err := repo.CancelJob(ctx, job2Input.TenantID, job2Input.ID); err != nil {
		t.Fatal(err)
	}

	// 3. Expired exhausted job becomes dead_letter
	job3Input := model.CreateJobInput{
		ID:             prefix + "-reclaim-dead",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-r3",
		AgentID:        "agent-1",
		MaxAttempts:    1,
		Request:        "r3",
	}
	if _, err := repo.CreateJob(ctx, job3Input); err != nil {
		t.Fatal(err)
	}
	_ = mustAcquireOne(t, repo, "worker-1")

	// Artificially expire all three leases in DB
	_, err := repo.pool.Exec(ctx, "UPDATE durable_jobs SET lease_expires_at = NOW() - INTERVAL '10 seconds' WHERE tenant_id = $1", prefix+"-tenant")
	if err != nil {
		t.Fatal(err)
	}

	reclaimed, err := repo.ReclaimExpiredLeases(ctx, 10, 5*time.Second)
	if err != nil {
		t.Fatalf("ReclaimExpiredLeases failed: %v", err)
	}
	if reclaimed < 3 {
		t.Fatalf("expected at least 3 reclaimed jobs, got %d", reclaimed)
	}

	// Verify states
	r1 := mustGetJob(t, repo, job1Input.TenantID, job1Input.ID)
	if r1.State != model.JobStateQueued || r1.LeaseOwner != nil || r1.LeaseToken <= leased1.LeaseToken {
		t.Fatalf("reclaimed job 1 invalid: %+v", r1)
	}

	r2 := mustGetJob(t, repo, job2Input.TenantID, job2Input.ID)
	if r2.State != model.JobStateCanceled || r2.LeaseOwner != nil {
		t.Fatalf("reclaimed job 2 invalid: %+v", r2)
	}

	r3 := mustGetJob(t, repo, job3Input.TenantID, job3Input.ID)
	if r3.State != model.JobStateDeadLetter || r3.LeaseOwner != nil {
		t.Fatalf("reclaimed job 3 invalid: %+v", r3)
	}

	// Stale worker completing old lease receives ErrLeaseLost
	err = repo.CompleteJob(ctx, leased1.Fence(), "late")
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost on stale worker complete after reclamation, got: %v", err)
	}
}

func TestIntegration_Jobs_Races_CancelVsComplete(t *testing.T) {
	repo, prefix := setupJobsIntegrationTest(t)
	ctx := context.Background()

	input := model.CreateJobInput{
		ID:             prefix + "-race-cancel-complete",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-race-key",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        "race",
	}
	if _, err := repo.CreateJob(ctx, input); err != nil {
		t.Fatal(err)
	}

	leased, _ := repo.AcquireLeases(ctx, "worker-race", 1, 30*time.Second)
	job := leased[0]

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_ = repo.CancelJob(ctx, job.TenantID, job.ID)
	}()

	go func() {
		defer wg.Done()
		_ = repo.CompleteJob(ctx, job.Fence(), "race-output")
	}()

	wg.Wait()

	finalJob, err := repo.GetJob(ctx, job.TenantID, job.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Must be either succeeded or canceled, never in an inconsistent corrupt state
	if finalJob.State != model.JobStateSucceeded && finalJob.State != model.JobStateCanceled {
		t.Fatalf("unexpected state after race: %s", finalJob.State)
	}
	if finalJob.State == model.JobStateSucceeded && len(finalJob.Response) == 0 {
		t.Fatal("succeeded job must contain response payload")
	}
}

func TestIntegration_Jobs_ContextCancellationAndMalformedJSON(t *testing.T) {
	repo, prefix := setupJobsIntegrationTest(t)

	// Context cancellation
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	input := model.CreateJobInput{
		ID:             prefix + "-ctx-cancel",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-ctx-key",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        "payload",
	}
	if _, err := repo.CreateJob(canceledCtx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on CreateJob, got: %v", err)
	}

	// Malformed JSON decode safety
	malformedID := prefix + "-malformed"
	_, err := repo.pool.Exec(
		context.Background(),
		`INSERT INTO durable_jobs (id, tenant_id, idempotency_key, request_hash, request, agent_id, state, attempt, max_attempts, next_run_at, lease_token, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, '{"bad": json'::text::jsonb, 'agent-1', 'queued', 0, 3, NOW(), 0, NOW(), NOW())`,
		malformedID,
		prefix+"-tenant",
		prefix+"-malformed-key",
		strings.Repeat("a", 64),
	)
	// If PostgreSQL rejects raw invalid JSON syntax (syntax error in json literal),
	// this is properly caught before insertion.
	if err != nil && !strings.Contains(err.Error(), "invalid input syntax for type json") {
		t.Fatalf("unexpected error inserting invalid JSON: %v", err)
	}
}

func TestIntegration_Jobs_ReviewedEdgeCases(t *testing.T) {
	t.Run("idempotency includes execution semantics", func(t *testing.T) {
		repo, prefix := setupJobsIntegrationTest(t)
		ctx := context.Background()
		base := model.CreateJobInput{
			ID: prefix + "-first", TenantID: prefix + "-tenant", IdempotencyKey: prefix + "-key",
			AgentID: "agent-a", MaxAttempts: 3, Request: map[string]any{"task": "same"},
		}
		if _, err := repo.CreateJob(ctx, base); err != nil {
			t.Fatal(err)
		}
		variant := base
		variant.ID = prefix + "-second"
		variant.AgentID = "agent-b"
		if _, err := repo.CreateJob(ctx, variant); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
		}
	})

	t.Run("job IDs are tenant scoped", func(t *testing.T) {
		repo, prefix := setupJobsIntegrationTest(t)
		ctx := context.Background()
		sharedID := prefix + "-shared-id"
		first := model.CreateJobInput{
			ID: sharedID, TenantID: prefix + "-tenant-a", IdempotencyKey: prefix + "-key-a",
			AgentID: "agent", MaxAttempts: 1, Request: "a",
		}
		second := model.CreateJobInput{
			ID: sharedID, TenantID: prefix + "-tenant-b", IdempotencyKey: prefix + "-key-b",
			AgentID: "agent", MaxAttempts: 1, Request: "b",
		}
		if _, err := repo.CreateJob(ctx, first); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.CreateJob(ctx, second); err != nil {
			t.Fatalf("same job ID in another tenant must succeed: %v", err)
		}
		conflict := first
		conflict.IdempotencyKey = prefix + "-another-key"
		if _, err := repo.CreateJob(ctx, conflict); !errors.Is(err, ErrJobIDConflict) {
			t.Fatalf("expected ErrJobIDConflict, got %v", err)
		}
	})

	t.Run("nil response is valid JSON null", func(t *testing.T) {
		repo, prefix := setupJobsIntegrationTest(t)
		ctx := context.Background()
		input := model.CreateJobInput{
			ID: prefix + "-nil", TenantID: prefix + "-tenant", IdempotencyKey: prefix + "-key",
			AgentID: "agent", MaxAttempts: 1, Request: "request",
		}
		if _, err := repo.CreateJob(ctx, input); err != nil {
			t.Fatal(err)
		}
		job := mustAcquireOne(t, repo, "worker")
		if err := repo.CompleteJob(ctx, job.Fence(), nil); err != nil {
			t.Fatal(err)
		}
		completed := mustGetJob(t, repo, input.TenantID, input.ID)
		if completed.State != model.JobStateSucceeded || string(completed.Response) != "null" {
			t.Fatalf("unexpected completed job: %+v", completed)
		}
	})

	t.Run("cancellation prevents leased job from starting", func(t *testing.T) {
		repo, prefix := setupJobsIntegrationTest(t)
		ctx := context.Background()
		input := model.CreateJobInput{
			ID: prefix + "-cancel", TenantID: prefix + "-tenant", IdempotencyKey: prefix + "-key",
			AgentID: "agent", MaxAttempts: 1, Request: "request",
		}
		if _, err := repo.CreateJob(ctx, input); err != nil {
			t.Fatal(err)
		}
		job := mustAcquireOne(t, repo, "worker")
		if err := repo.CancelJob(ctx, input.TenantID, input.ID); err != nil {
			t.Fatal(err)
		}
		if err := repo.MarkRunning(ctx, job.Fence()); !errors.Is(err, ErrCancellationRequested) {
			t.Fatalf("expected ErrCancellationRequested, got %v", err)
		}
	})
}

func TestIntegration_Jobs_ErrorNonLeakage(t *testing.T) {
	dsn := getTestDSN(t)
	tokens := extractRedactTokens(dsn)

	rawErr := fmt.Errorf("connection error with %s and credentials", dsn)
	redacted := redactError(rawErr, tokens)

	if strings.Contains(redacted.Error(), dsn) {
		t.Fatalf("redacted error leaked raw DSN: %s", redacted.Error())
	}
}
