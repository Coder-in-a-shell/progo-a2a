package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/storage"
)

type fakeRepo struct {
	mu sync.Mutex

	acquireFunc     func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error)
	markRunningFunc func(ctx context.Context, fence model.LeaseFence) error
	renewLeaseFunc  func(ctx context.Context, fence model.LeaseFence, duration time.Duration) error
	completeJobFunc func(ctx context.Context, fence model.LeaseFence, response any) error
	failJobFunc     func(ctx context.Context, input model.FailJobInput) error
	cancelJobFunc   func(ctx context.Context, tenantID, jobID string) error
	ackCancelFunc   func(ctx context.Context, fence model.LeaseFence) error
	reclaimFunc     func(ctx context.Context, batchSize int, defaultBackoff time.Duration) (int, error)
	getJobFunc      func(ctx context.Context, tenantID, jobID string) (*model.Job, error)
	createJobFunc   func(ctx context.Context, input model.CreateJobInput) (*model.Job, error)

	markRunningCalls []model.LeaseFence
	renewLeaseCalls  []model.LeaseFence
	completeJobCalls []model.LeaseFence
	failJobCalls     []model.FailJobInput
	ackCancelCalls   []model.LeaseFence
	acquireCallCount int
}

func (f *fakeRepo) CreateJob(ctx context.Context, input model.CreateJobInput) (*model.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createJobFunc != nil {
		return f.createJobFunc(ctx, input)
	}
	return nil, nil
}

func (f *fakeRepo) GetJob(ctx context.Context, tenantID, jobID string) (*model.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getJobFunc != nil {
		return f.getJobFunc(ctx, tenantID, jobID)
	}
	return nil, nil
}

func (f *fakeRepo) AcquireLeases(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
	f.mu.Lock()
	f.acquireCallCount++
	fn := f.acquireFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, owner, batchSize, leaseDuration)
	}
	return nil, nil
}

func (f *fakeRepo) MarkRunning(ctx context.Context, fence model.LeaseFence) error {
	f.mu.Lock()
	f.markRunningCalls = append(f.markRunningCalls, fence)
	fn := f.markRunningFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, fence)
	}
	return nil
}

func (f *fakeRepo) RenewLease(ctx context.Context, fence model.LeaseFence, duration time.Duration) error {
	f.mu.Lock()
	f.renewLeaseCalls = append(f.renewLeaseCalls, fence)
	fn := f.renewLeaseFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, fence, duration)
	}
	return nil
}

func (f *fakeRepo) CompleteJob(ctx context.Context, fence model.LeaseFence, response any) error {
	f.mu.Lock()
	f.completeJobCalls = append(f.completeJobCalls, fence)
	fn := f.completeJobFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, fence, response)
	}
	return nil
}

func (f *fakeRepo) FailJob(ctx context.Context, input model.FailJobInput) error {
	f.mu.Lock()
	f.failJobCalls = append(f.failJobCalls, input)
	fn := f.failJobFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, input)
	}
	return nil
}

func (f *fakeRepo) CancelJob(ctx context.Context, tenantID, jobID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelJobFunc != nil {
		return f.cancelJobFunc(ctx, tenantID, jobID)
	}
	return nil
}

func (f *fakeRepo) AcknowledgeCancellation(ctx context.Context, fence model.LeaseFence) error {
	f.mu.Lock()
	f.ackCancelCalls = append(f.ackCancelCalls, fence)
	fn := f.ackCancelFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, fence)
	}
	return nil
}

func (f *fakeRepo) ReclaimExpiredLeases(ctx context.Context, batchSize int, defaultBackoff time.Duration) (int, error) {
	f.mu.Lock()
	fn := f.reclaimFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, batchSize, defaultBackoff)
	}
	return 0, nil
}

type fakeExec struct {
	execFunc func(ctx context.Context, job *model.Job) (any, error)
}

func (f *fakeExec) Execute(ctx context.Context, job *model.Job) (any, error) {
	if f.execFunc != nil {
		return f.execFunc(ctx, job)
	}
	return "ok", nil
}

func testWorkerConfig() config.WorkerConfig {
	return config.WorkerConfig{
		WorkerID:                 "test-worker-1",
		Concurrency:              2,
		BatchSize:                2,
		PollIntervalMilliseconds: 10,
		LeaseDurationSeconds:     30,
		RenewalIntervalSeconds:   10,
		RetryBackoffSeconds:      15,
		DrainTimeoutSeconds:      2,
	}
}

func makeTestJob(id string, maxAttempts int) *model.Job {
	owner := "test-worker-1"
	expires := time.Now().Add(30 * time.Second)
	return &model.Job{
		ID:             id,
		TenantID:       "tenant-test",
		AgentID:        "agent-test",
		State:          model.JobStateLeased,
		Attempt:        1,
		MaxAttempts:    maxAttempts,
		LeaseOwner:     &owner,
		LeaseToken:     1,
		LeaseExpiresAt: &expires,
		Request:        []byte(`{"task":"test"}`),
	}
}

// 1. Concurrency and Capacity bound
func TestWorker_ConcurrencyCapacityBound(t *testing.T) {
	cfg := testWorkerConfig()
	cfg.Concurrency = 2
	cfg.BatchSize = 2

	var runningJobs atomic.Int32
	var maxConcurrent atomic.Int32

	blockCh := make(chan struct{})
	exec := &fakeExec{
		execFunc: func(ctx context.Context, job *model.Job) (any, error) {
			cur := runningJobs.Add(1)
			defer runningJobs.Add(-1)
			for {
				old := maxConcurrent.Load()
				if cur <= old || maxConcurrent.CompareAndSwap(old, cur) {
					break
				}
			}
			<-blockCh
			return "done", nil
		},
	}

	repo := &fakeRepo{}
	var jobsServed atomic.Int32
	repo.acquireFunc = func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
		if batchSize > 2 {
			t.Errorf("batchSize %d exceeds concurrency 2", batchSize)
		}
		n := jobsServed.Add(int32(batchSize))
		if n <= 4 {
			return []*model.Job{
				makeTestJob("job-1", 1),
				makeTestJob("job-2", 1),
			}, nil
		}
		return nil, nil
	}

	eng := NewEngine(cfg, repo, exec)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.Run(ctx)
	}()

	// Wait for 2 jobs to be running concurrently
	for i := 0; i < 50; i++ {
		if runningJobs.Load() == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if runningJobs.Load() != 2 {
		t.Fatalf("expected 2 concurrent jobs, got %d", runningJobs.Load())
	}

	// Unblock running jobs and cancel context
	close(blockCh)
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("engine failed to exit in time")
	}

	if maxConcurrent.Load() > 2 {
		t.Errorf("max concurrent jobs %d exceeded concurrency bound 2", maxConcurrent.Load())
	}
}

// 2. Empty polling
func TestWorker_EmptyPolling(t *testing.T) {
	cfg := testWorkerConfig()
	repo := &fakeRepo{
		acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
			return nil, nil
		},
	}
	eng := NewEngine(cfg, repo, &fakeExec{})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := eng.Run(ctx); err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if repo.acquireCallCount == 0 {
		t.Error("expected at least one acquire call during polling")
	}
}

// 3. Lease -> running -> success
func TestWorker_LeaseRunningSuccess(t *testing.T) {
	cfg := testWorkerConfig()
	job := makeTestJob("job-success", 1)

	var acquired atomic.Bool
	repo := &fakeRepo{
		acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
			if acquired.CompareAndSwap(false, true) {
				return []*model.Job{job}, nil
			}
			return nil, nil
		},
	}

	exec := &fakeExec{
		execFunc: func(ctx context.Context, j *model.Job) (any, error) {
			return map[string]string{"output": "success"}, nil
		},
	}

	eng := NewEngine(cfg, repo, exec)
	ctx, cancel := context.WithCancel(context.Background())

	eng.onJobCompleted = func(j *model.Job) {
		cancel()
	}

	if err := eng.Run(ctx); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()

	if len(repo.markRunningCalls) != 1 {
		t.Fatalf("expected 1 MarkRunning call, got %d", len(repo.markRunningCalls))
	}
	if repo.markRunningCalls[0].JobID != "job-success" {
		t.Errorf("expected MarkRunning for job-success, got %s", repo.markRunningCalls[0].JobID)
	}
	if len(repo.completeJobCalls) != 1 {
		t.Fatalf("expected 1 CompleteJob call, got %d", len(repo.completeJobCalls))
	}
	if repo.completeJobCalls[0].JobID != "job-success" {
		t.Errorf("expected CompleteJob for job-success, got %s", repo.completeJobCalls[0].JobID)
	}
}

// 4. Retryable and non-retryable failure
func TestWorker_RetryableAndNonRetryableFailure(t *testing.T) {
	t.Run("retryable failure with max_attempts > 1", func(t *testing.T) {
		cfg := testWorkerConfig()
		job := makeTestJob("job-retryable", 3)

		var acquired atomic.Bool
		repo := &fakeRepo{
			acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
				if acquired.CompareAndSwap(false, true) {
					return []*model.Job{job}, nil
				}
				return nil, nil
			},
		}

		exec := &fakeExec{
			execFunc: func(ctx context.Context, j *model.Job) (any, error) {
				return nil, &ExecutionError{
					Code:      "DOWNSTREAM_TIMEOUT",
					Message:   "downstream timeout",
					Retryable: true,
				}
			},
		}

		eng := NewEngine(cfg, repo, exec)
		ctx, cancel := context.WithCancel(context.Background())
		eng.onJobFailed = func(j *model.Job, retryable bool) {
			cancel()
		}

		if err := eng.Run(ctx); err != nil {
			t.Fatalf("Run returned error: %v", err)
		}

		repo.mu.Lock()
		defer repo.mu.Unlock()

		if len(repo.failJobCalls) != 1 {
			t.Fatalf("expected 1 FailJob call, got %d", len(repo.failJobCalls))
		}
		if !repo.failJobCalls[0].Retryable {
			t.Errorf("expected FailJob Retryable=true for max_attempts=3, got false")
		}
		if repo.failJobCalls[0].FailureCode != "DOWNSTREAM_TIMEOUT" {
			t.Errorf("expected code DOWNSTREAM_TIMEOUT, got %s", repo.failJobCalls[0].FailureCode)
		}
	})

	t.Run("non-retryable failure with max_attempts = 1", func(t *testing.T) {
		cfg := testWorkerConfig()
		job := makeTestJob("job-non-retryable", 1)

		var acquired atomic.Bool
		repo := &fakeRepo{
			acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
				if acquired.CompareAndSwap(false, true) {
					return []*model.Job{job}, nil
				}
				return nil, nil
			},
		}

		exec := &fakeExec{
			execFunc: func(ctx context.Context, j *model.Job) (any, error) {
				return nil, &ExecutionError{
					Code:      "DOWNSTREAM_TIMEOUT",
					Message:   "downstream timeout",
					Retryable: true,
				}
			},
		}

		eng := NewEngine(cfg, repo, exec)
		ctx, cancel := context.WithCancel(context.Background())
		eng.onJobFailed = func(j *model.Job, retryable bool) {
			cancel()
		}

		if err := eng.Run(ctx); err != nil {
			t.Fatalf("Run returned error: %v", err)
		}

		repo.mu.Lock()
		defer repo.mu.Unlock()

		if len(repo.failJobCalls) != 1 {
			t.Fatalf("expected 1 FailJob call, got %d", len(repo.failJobCalls))
		}
		if repo.failJobCalls[0].Retryable {
			t.Errorf("expected FailJob Retryable=false for max_attempts=1, got true")
		}
	})
}

// 5. Panic recovery
func TestWorker_PanicRecovery(t *testing.T) {
	cfg := testWorkerConfig()
	job := makeTestJob("job-panic", 1)

	var acquired atomic.Bool
	repo := &fakeRepo{
		acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
			if acquired.CompareAndSwap(false, true) {
				return []*model.Job{job}, nil
			}
			return nil, nil
		},
	}

	exec := &fakeExec{
		execFunc: func(ctx context.Context, j *model.Job) (any, error) {
			panic("unexpected nil pointer exception")
		},
	}

	eng := NewEngine(cfg, repo, exec)
	ctx, cancel := context.WithCancel(context.Background())
	eng.onJobFailed = func(j *model.Job, retryable bool) {
		cancel()
	}

	// Must NOT crash the process
	if err := eng.Run(ctx); err != nil {
		t.Fatalf("Run returned error after panic recovery: %v", err)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()

	if len(repo.failJobCalls) != 1 {
		t.Fatalf("expected 1 FailJob call on panic, got %d", len(repo.failJobCalls))
	}
	fail := repo.failJobCalls[0]
	if fail.FailureCode != "PANIC" {
		t.Errorf("expected FailureCode PANIC, got %s", fail.FailureCode)
	}
	if fail.FailureMessage != "internal execution error occurred" {
		t.Errorf("expected sanitized generic message, got %s", fail.FailureMessage)
	}
	if fail.Retryable {
		t.Error("expected Retryable=false on panic")
	}
}

func TestWorker_PanicRecovery_DoesNotLogSecret(t *testing.T) {
	cfg := testWorkerConfig()
	job := makeTestJob("job-secret-panic", 1)

	var acquired atomic.Bool
	repo := &fakeRepo{
		acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
			if acquired.CompareAndSwap(false, true) {
				return []*model.Job{job}, nil
			}
			return nil, nil
		},
	}

	secretToken := "SUPER_SECRET_TOKEN_XYZ_12345"
	exec := &fakeExec{
		execFunc: func(ctx context.Context, j *model.Job) (any, error) {
			panic(secretToken)
		},
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	eng := NewEngine(cfg, repo, exec, WithLogger(logger))
	ctx, cancel := context.WithCancel(context.Background())
	eng.onJobFailed = func(j *model.Job, retryable bool) {
		cancel()
	}

	if err := eng.Run(ctx); err != nil {
		t.Fatalf("Run returned error after panic recovery: %v", err)
	}

	logOutput := logBuf.String()
	if strings.Contains(logOutput, secretToken) {
		t.Fatalf("log output contained sensitive panic value: %s", logOutput)
	}
	if !strings.Contains(logOutput, "job execution panicked; recovering generic failure") {
		t.Errorf("expected generic log event in logs, got: %s", logOutput)
	}
}

// 6. Cancellation acknowledgement
func TestWorker_CancellationAcknowledgement(t *testing.T) {
	t.Run("detected before running at MarkRunning", func(t *testing.T) {
		cfg := testWorkerConfig()
		job := makeTestJob("job-cancel-start", 1)

		var acquired atomic.Bool
		repo := &fakeRepo{
			acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
				if acquired.CompareAndSwap(false, true) {
					return []*model.Job{job}, nil
				}
				return nil, nil
			},
			markRunningFunc: func(ctx context.Context, fence model.LeaseFence) error {
				return storage.ErrCancellationRequested
			},
		}

		executed := false
		exec := &fakeExec{
			execFunc: func(ctx context.Context, j *model.Job) (any, error) {
				executed = true
				return "done", nil
			},
		}

		eng := NewEngine(cfg, repo, exec)
		ctx, cancel := context.WithCancel(context.Background())
		eng.onJobCanceled = func(j *model.Job) {
			cancel()
		}

		if err := eng.Run(ctx); err != nil {
			t.Fatalf("Run returned error: %v", err)
		}

		if executed {
			t.Error("executor must not be invoked when cancellation is requested at MarkRunning")
		}

		repo.mu.Lock()
		defer repo.mu.Unlock()
		if len(repo.ackCancelCalls) != 1 {
			t.Fatalf("expected 1 AcknowledgeCancellation call, got %d", len(repo.ackCancelCalls))
		}
	})

	t.Run("cancellation ack error before running is handled", func(t *testing.T) {
		cfg := testWorkerConfig()
		job := makeTestJob("job-cancel-ack-err", 1)

		var acquired atomic.Bool
		repo := &fakeRepo{
			acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
				if acquired.CompareAndSwap(false, true) {
					return []*model.Job{job}, nil
				}
				return nil, nil
			},
			markRunningFunc: func(ctx context.Context, fence model.LeaseFence) error {
				return storage.ErrCancellationRequested
			},
			ackCancelFunc: func(ctx context.Context, fence model.LeaseFence) error {
				return errors.New("db ack failure")
			},
		}

		executed := false
		exec := &fakeExec{
			execFunc: func(ctx context.Context, j *model.Job) (any, error) {
				executed = true
				return "done", nil
			},
		}

		eng := NewEngine(cfg, repo, exec)
		ctx, cancel := context.WithCancel(context.Background())
		canceledFired := false
		eng.onJobCanceled = func(j *model.Job) {
			canceledFired = true
		}

		time.AfterFunc(100*time.Millisecond, cancel)

		if err := eng.Run(ctx); err != nil {
			t.Fatalf("Run returned error: %v", err)
		}

		if executed {
			t.Error("executor must not run when cancellation requested, even if ack failed")
		}
		if canceledFired {
			t.Error("onJobCanceled must not fire if AcknowledgeCancellation returned error")
		}
	})

	t.Run("detected during renewal via GetJob", func(t *testing.T) {
		cfg := testWorkerConfig()
		cfg.RenewalIntervalSeconds = 1
		job := makeTestJob("job-cancel-renew", 1)

		var acquired atomic.Bool
		now := time.Now()
		repo := &fakeRepo{
			acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
				if acquired.CompareAndSwap(false, true) {
					return []*model.Job{job}, nil
				}
				return nil, nil
			},
			getJobFunc: func(ctx context.Context, tenantID, jobID string) (*model.Job, error) {
				j := *job
				j.CancelRequestedAt = &now
				return &j, nil
			},
		}

		var jobCtxCanceled atomic.Bool
		exec := &fakeExec{
			execFunc: func(ctx context.Context, j *model.Job) (any, error) {
				<-ctx.Done()
				jobCtxCanceled.Store(true)
				return nil, ctx.Err()
			},
		}

		eng := NewEngine(cfg, repo, exec)
		ctx, cancel := context.WithCancel(context.Background())
		eng.onJobCanceled = func(j *model.Job) {
			cancel()
		}

		if err := eng.Run(ctx); err != nil {
			t.Fatalf("Run returned error: %v", err)
		}

		repo.mu.Lock()
		defer repo.mu.Unlock()
		if len(repo.ackCancelCalls) != 1 {
			t.Fatalf("expected 1 AcknowledgeCancellation call during renewal, got %d", len(repo.ackCancelCalls))
		}
	})

	t.Run("cancellation ack error during renewal is handled", func(t *testing.T) {
		cfg := testWorkerConfig()
		cfg.RenewalIntervalSeconds = 1
		job := makeTestJob("job-cancel-renew-ack-err", 1)

		var acquired atomic.Bool
		now := time.Now()
		repo := &fakeRepo{
			acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
				if acquired.CompareAndSwap(false, true) {
					return []*model.Job{job}, nil
				}
				return nil, nil
			},
			getJobFunc: func(ctx context.Context, tenantID, jobID string) (*model.Job, error) {
				j := *job
				j.CancelRequestedAt = &now
				return &j, nil
			},
			ackCancelFunc: func(ctx context.Context, fence model.LeaseFence) error {
				return errors.New("db renewal ack failure")
			},
		}

		var jobCtxCanceled atomic.Bool
		exec := &fakeExec{
			execFunc: func(ctx context.Context, j *model.Job) (any, error) {
				<-ctx.Done()
				jobCtxCanceled.Store(true)
				return nil, ctx.Err()
			},
		}

		eng := NewEngine(cfg, repo, exec)
		ctx, cancel := context.WithCancel(context.Background())
		canceledFired := false
		eng.onJobCanceled = func(j *model.Job) {
			canceledFired = true
		}

		time.AfterFunc(1500*time.Millisecond, cancel)

		if err := eng.Run(ctx); err != nil {
			t.Fatalf("Run returned error: %v", err)
		}

		if canceledFired {
			t.Error("onJobCanceled must not fire if AcknowledgeCancellation returned error")
		}
		if !jobCtxCanceled.Load() {
			t.Error("job execution context must be canceled when cancellation intent is known")
		}
	})
}

// 7. Lease renewal
func TestWorker_LeaseRenewal(t *testing.T) {
	cfg := testWorkerConfig()
	cfg.RenewalIntervalSeconds = 1 // 1 second renewal
	job := makeTestJob("job-renew", 1)

	var acquired atomic.Bool
	renewCalled := make(chan struct{}, 1)
	repo := &fakeRepo{
		acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
			if acquired.CompareAndSwap(false, true) {
				return []*model.Job{job}, nil
			}
			return nil, nil
		},
		renewLeaseFunc: func(ctx context.Context, fence model.LeaseFence, duration time.Duration) error {
			select {
			case renewCalled <- struct{}{}:
			default:
			}
			return nil
		},
	}

	exec := &fakeExec{
		execFunc: func(ctx context.Context, j *model.Job) (any, error) {
			select {
			case <-renewCalled:
				return "done", nil
			case <-time.After(3 * time.Second):
				return nil, errors.New("timeout waiting for renewal")
			}
		},
	}

	eng := NewEngine(cfg, repo, exec)
	ctx, cancel := context.WithCancel(context.Background())
	eng.onJobCompleted = func(j *model.Job) {
		cancel()
	}

	if err := eng.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.renewLeaseCalls) < 1 {
		t.Fatalf("expected at least 1 RenewLease call, got %d", len(repo.renewLeaseCalls))
	}
}

// 8. Lease loss cancels job context
func TestWorker_LeaseLoss(t *testing.T) {
	cfg := testWorkerConfig()
	cfg.RenewalIntervalSeconds = 1
	job := makeTestJob("job-lease-loss", 1)

	var acquired atomic.Bool
	repo := &fakeRepo{
		acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
			if acquired.CompareAndSwap(false, true) {
				return []*model.Job{job}, nil
			}
			return nil, nil
		},
		renewLeaseFunc: func(ctx context.Context, fence model.LeaseFence, duration time.Duration) error {
			return storage.ErrLeaseLost
		},
	}

	contextLost := make(chan struct{})
	exec := &fakeExec{
		execFunc: func(ctx context.Context, j *model.Job) (any, error) {
			select {
			case <-ctx.Done():
				close(contextLost)
				return nil, ctx.Err()
			case <-time.After(3 * time.Second):
				return "done", nil
			}
		},
	}

	eng := NewEngine(cfg, repo, exec)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.Run(ctx)
	}()

	select {
	case <-contextLost:
		// Context was canceled immediately on ErrLeaseLost!
	case <-time.After(3 * time.Second):
		t.Fatal("expected execution context to be canceled on lease loss")
	}

	cancel()
	<-errCh

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.completeJobCalls) != 0 {
		t.Errorf("must not call CompleteJob after lease lost, got %d calls", len(repo.completeJobCalls))
	}
	if len(repo.failJobCalls) != 0 {
		t.Errorf("must not call FailJob when aborted due to lease loss, got %d calls", len(repo.failJobCalls))
	}
}

// 9. Acquisition and Reclamation transient errors
func TestWorker_TransientErrorsHandled(t *testing.T) {
	cfg := testWorkerConfig()
	cfg.RetryBackoffSeconds = 1

	var acquireTries atomic.Int32
	var reclaimTries atomic.Int32

	job := makeTestJob("job-transient", 1)
	repo := &fakeRepo{
		acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
			if acquireTries.Add(1) == 1 {
				return nil, errors.New("db connection refused")
			}
			if acquireTries.Load() == 2 {
				return []*model.Job{job}, nil
			}
			return nil, nil
		},
		reclaimFunc: func(ctx context.Context, batchSize int, defaultBackoff time.Duration) (int, error) {
			if reclaimTries.Add(1) == 1 {
				return 0, errors.New("db lock timeout")
			}
			return 0, nil
		},
	}

	eng := NewEngine(cfg, repo, &fakeExec{})
	ctx, cancel := context.WithCancel(context.Background())
	eng.onJobCompleted = func(j *model.Job) {
		cancel()
	}

	if err := eng.Run(ctx); err != nil {
		t.Fatalf("Run returned error despite transient recovery: %v", err)
	}

	if acquireTries.Load() < 2 {
		t.Errorf("expected multiple acquire attempts after transient error, got %d", acquireTries.Load())
	}
}

// 10. Shutdown drain success
func TestWorker_ShutdownDrainSuccess(t *testing.T) {
	cfg := testWorkerConfig()
	cfg.DrainTimeoutSeconds = 2
	job := makeTestJob("job-drain", 1)

	var acquired atomic.Bool
	jobRunning := make(chan struct{})
	repo := &fakeRepo{
		acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
			if acquired.CompareAndSwap(false, true) {
				return []*model.Job{job}, nil
			}
			return nil, nil
		},
	}

	exec := &fakeExec{
		execFunc: func(ctx context.Context, j *model.Job) (any, error) {
			close(jobRunning)
			time.Sleep(100 * time.Millisecond) // finishes well within 2s drain
			return "drained", nil
		},
	}

	eng := NewEngine(cfg, repo, exec)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.Run(ctx)
	}()

	// Wait until job is executing, then initiate shutdown
	<-jobRunning
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("engine failed to drain and shutdown in time")
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.completeJobCalls) != 1 {
		t.Fatalf("expected 1 CompleteJob call for drained job, got %d", len(repo.completeJobCalls))
	}
}

// 11. Shutdown deadline cancels remaining execution without writing failure
func TestWorker_ShutdownDeadline(t *testing.T) {
	cfg := testWorkerConfig()
	cfg.DrainTimeoutSeconds = 1 // 1 second drain timeout
	job := makeTestJob("job-slow", 1)

	var acquired atomic.Bool
	jobStarted := make(chan struct{})
	releaseCh := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseCh:
		default:
			close(releaseCh)
		}
	})

	repo := &fakeRepo{
		acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
			if acquired.CompareAndSwap(false, true) {
				return []*model.Job{job}, nil
			}
			return nil, nil
		},
	}

	// Adversarial executor: intentionally ignores context cancellation and blocks on releaseCh
	exec := &fakeExec{
		execFunc: func(ctx context.Context, j *model.Job) (any, error) {
			close(jobStarted)
			<-releaseCh
			return "adversarial-late-result", nil
		},
	}

	eng := NewEngine(cfg, repo, exec)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	start := time.Now()
	go func() {
		errCh <- eng.Run(ctx)
	}()

	<-jobStarted
	cancel() // initiate shutdown

	// Run must return at the deadline without an unbounded wait on drainDone
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("engine failed to exit after drain timeout; wait was unbounded")
	}

	elapsed := time.Since(start)
	if elapsed < 900*time.Millisecond {
		t.Errorf("expected engine to wait for drain timeout (~1s), exited in %v", elapsed)
	}

	repo.mu.Lock()
	if len(repo.completeJobCalls) != 0 {
		t.Errorf("expected 0 CompleteJob calls on drain deadline, got %d", len(repo.completeJobCalls))
	}
	if len(repo.failJobCalls) != 0 {
		t.Errorf("expected 0 FailJob calls on drain deadline, got %d", len(repo.failJobCalls))
	}
	repo.mu.Unlock()

	// Release executor so no goroutine leaks remain
	close(releaseCh)
	time.Sleep(50 * time.Millisecond)

	// Even after executor returns, state must never be written after shutdown cancellation
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.completeJobCalls) != 0 {
		t.Errorf("expected 0 CompleteJob calls after adversarial completion, got %d", len(repo.completeJobCalls))
	}
	if len(repo.failJobCalls) != 0 {
		t.Errorf("expected 0 FailJob calls after adversarial completion, got %d", len(repo.failJobCalls))
	}
}

// 12. No new leases after shutdown starts
func TestWorker_NoNewLeasesAfterShutdownStarts(t *testing.T) {
	cfg := testWorkerConfig()
	cfg.DrainTimeoutSeconds = 2
	job := makeTestJob("job-no-new-leases", 1)

	var acquired atomic.Bool
	var acquireAfterShutdown atomic.Bool

	ctx, cancel := context.WithCancel(context.Background())

	repo := &fakeRepo{
		acquireFunc: func(c context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
			if acquired.CompareAndSwap(false, true) {
				return []*model.Job{job}, nil
			}
			if ctx.Err() != nil {
				acquireAfterShutdown.Store(true)
			}
			return nil, nil
		},
	}

	jobRunning := make(chan struct{})
	exec := &fakeExec{
		execFunc: func(c context.Context, j *model.Job) (any, error) {
			close(jobRunning)
			time.Sleep(100 * time.Millisecond)
			return "done", nil
		},
	}

	eng := NewEngine(cfg, repo, exec)

	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.Run(ctx)
	}()

	<-jobRunning
	cancel() // Initiate shutdown

	<-errCh

	if acquireAfterShutdown.Load() {
		t.Error("AcquireLeases was called after shutdown started")
	}
}

// 13. Close lease-after-shutdown race
func TestWorker_CloseLeaseAfterShutdownRace(t *testing.T) {
	cfg := testWorkerConfig()
	job := makeTestJob("race-job", 1)

	acquireEntered := make(chan struct{})
	acquireBlock := make(chan struct{})
	var markRunningCalled atomic.Bool
	var executeCalled atomic.Bool

	repo := &fakeRepo{
		acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
			select {
			case <-acquireEntered:
			default:
				close(acquireEntered)
			}
			<-acquireBlock
			return []*model.Job{job}, nil
		},
		markRunningFunc: func(ctx context.Context, fence model.LeaseFence) error {
			markRunningCalled.Store(true)
			return nil
		},
	}

	exec := &fakeExec{
		execFunc: func(ctx context.Context, j *model.Job) (any, error) {
			executeCalled.Store(true)
			return "done", nil
		},
	}

	eng := NewEngine(cfg, repo, exec)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.Run(ctx)
	}()

	// Wait until AcquireLeases is blocked
	<-acquireEntered

	// Cancel parent context while AcquireLeases is in flight
	cancel()

	// Release AcquireLeases so it returns the job
	close(acquireBlock)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("engine timed out during shutdown")
	}

	if markRunningCalled.Load() {
		t.Error("MarkRunning must NOT be called when parent cancellation occurred during AcquireLeases")
	}
	if executeCalled.Load() {
		t.Error("Execute must NOT be called when parent cancellation occurred during AcquireLeases")
	}
}

// 14. Engine input validation
func TestWorker_Run_InputValidation(t *testing.T) {
	validCfg := testWorkerConfig()
	repo := &fakeRepo{}
	exec := &fakeExec{}

	tests := []struct {
		name        string
		cfg         config.WorkerConfig
		repo        storage.JobRepository
		exec        JobExecutor
		errContains string
	}{
		{
			name:        "nil repository",
			cfg:         validCfg,
			repo:        nil,
			exec:        exec,
			errContains: "job repository cannot be nil",
		},
		{
			name:        "nil executor",
			cfg:         validCfg,
			repo:        repo,
			exec:        nil,
			errContains: "job executor cannot be nil",
		},
		{
			name: "non-positive concurrency",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.Concurrency = 0
				return c
			}(),
			repo:        repo,
			exec:        exec,
			errContains: "worker concurrency must be positive",
		},
		{
			name: "non-positive batch_size",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.BatchSize = 0
				return c
			}(),
			repo:        repo,
			exec:        exec,
			errContains: "worker batch_size must be positive",
		},
		{
			name: "batch_size exceeds concurrency",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.Concurrency = 2
				c.BatchSize = 5
				return c
			}(),
			repo:        repo,
			exec:        exec,
			errContains: "must not exceed concurrency",
		},
		{
			name: "non-positive poll interval",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.PollIntervalMilliseconds = 0
				return c
			}(),
			repo:        repo,
			exec:        exec,
			errContains: "worker poll_interval_milliseconds must be positive",
		},
		{
			name: "lease duration below minimum",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.LeaseDurationSeconds = 0
				return c
			}(),
			repo:        repo,
			exec:        exec,
			errContains: "worker lease_duration_seconds must be at least 2",
		},
		{
			name: "non-positive renewal interval",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.RenewalIntervalSeconds = 0
				return c
			}(),
			repo:        repo,
			exec:        exec,
			errContains: "worker renewal_interval_seconds must be positive",
		},
		{
			name: "renewal interval more than half lease duration",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.LeaseDurationSeconds = 30
				c.RenewalIntervalSeconds = 20
				return c
			}(),
			repo:        repo,
			exec:        exec,
			errContains: "must be at most half of lease_duration_seconds",
		},
		{
			name: "non-positive retry backoff",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.RetryBackoffSeconds = 0
				return c
			}(),
			repo:        repo,
			exec:        exec,
			errContains: "worker retry_backoff_seconds must be positive",
		},
		{
			name: "non-positive drain timeout",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.DrainTimeoutSeconds = 0
				return c
			}(),
			repo:        repo,
			exec:        exec,
			errContains: "worker drain_timeout_seconds must be positive",
		},
		{
			name: "concurrency above maximum",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.Concurrency = storage.MaxJobBatchSize + 1
				return c
			}(),
			repo: repo, exec: exec, errContains: "worker concurrency must not exceed",
		},
		{
			name: "poll interval above maximum",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.PollIntervalMilliseconds = config.MaxWorkerPollIntervalMilliseconds + 1
				return c
			}(),
			repo: repo, exec: exec, errContains: "worker poll_interval_milliseconds must not exceed",
		},
		{
			name: "lease duration above maximum",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.LeaseDurationSeconds = int(storage.MaxJobLeaseDuration.Seconds()) + 1
				return c
			}(),
			repo: repo, exec: exec, errContains: "worker lease_duration_seconds must not exceed",
		},
		{
			name: "retry backoff above maximum",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.RetryBackoffSeconds = int(model.MaxRetryBackoff.Seconds()) + 1
				return c
			}(),
			repo: repo, exec: exec, errContains: "worker retry_backoff_seconds must not exceed",
		},
		{
			name: "drain timeout above maximum",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.DrainTimeoutSeconds = config.MaxWorkerDrainTimeoutSeconds + 1
				return c
			}(),
			repo: repo, exec: exec, errContains: "worker drain_timeout_seconds must not exceed",
		},
		{
			name: "blank worker ID",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.WorkerID = " \t\n "
				return c
			}(),
			repo: repo, exec: exec, errContains: "worker ID cannot be blank",
		},
		{
			name: "worker ID above maximum",
			cfg: func() config.WorkerConfig {
				c := validCfg
				c.WorkerID = strings.Repeat("w", model.MaxLeaseOwnerLength+1)
				return c
			}(),
			repo: repo, exec: exec, errContains: "worker ID must not exceed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eng := NewEngine(tc.cfg, tc.repo, tc.exec)
			err := eng.Run(context.Background())
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("expected error containing %q, got %q", tc.errContains, err.Error())
			}
		})
	}
}

func TestWorker_Run_NilContext(t *testing.T) {
	eng := NewEngine(testWorkerConfig(), &fakeRepo{}, &fakeExec{})
	if err := eng.Run(nil); err == nil || !strings.Contains(err.Error(), "worker context cannot be nil") {
		t.Fatalf("expected nil-context validation error, got %v", err)
	}
}

// 15. Acquired results hardening: nil jobs and over-returned jobs
func TestWorker_AcquiredResults_NilAndOverReturnedJobs(t *testing.T) {
	t.Run("nil job discarded without panic or capacity leak", func(t *testing.T) {
		cfg := testWorkerConfig()
		cfg.Concurrency = 2
		cfg.BatchSize = 2
		validJob := makeTestJob("valid-job", 1)

		var acquired atomic.Bool
		repo := &fakeRepo{
			acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
				if acquired.CompareAndSwap(false, true) {
					// Rogue repo returns nil element alongside valid job
					return []*model.Job{nil, validJob}, nil
				}
				return nil, nil
			},
		}

		exec := &fakeExec{}
		eng := NewEngine(cfg, repo, exec)
		ctx, cancel := context.WithCancel(context.Background())
		eng.onJobCompleted = func(j *model.Job) {
			cancel()
		}

		if err := eng.Run(ctx); err != nil {
			t.Fatalf("Run failed: %v", err)
		}

		repo.mu.Lock()
		defer repo.mu.Unlock()
		if len(repo.completeJobCalls) != 1 {
			t.Fatalf("expected 1 CompleteJob call for valid job, got %d", len(repo.completeJobCalls))
		}
	})

	t.Run("over-returned jobs truncated without overfilling semaphore", func(t *testing.T) {
		cfg := testWorkerConfig()
		cfg.Concurrency = 2
		cfg.BatchSize = 2

		var acquired atomic.Bool
		repo := &fakeRepo{
			acquireFunc: func(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
				if acquired.CompareAndSwap(false, true) {
					// Rogue repo returns 5 jobs when batch size is 2
					return []*model.Job{
						makeTestJob("job-1", 1),
						makeTestJob("job-2", 1),
						makeTestJob("job-3", 1),
						makeTestJob("job-4", 1),
						makeTestJob("job-5", 1),
					}, nil
				}
				return nil, nil
			},
		}

		var completedCount atomic.Int32
		exec := &fakeExec{}
		eng := NewEngine(cfg, repo, exec)
		ctx, cancel := context.WithCancel(context.Background())
		eng.onJobCompleted = func(j *model.Job) {
			if completedCount.Add(1) == 2 {
				cancel()
			}
		}

		if err := eng.Run(ctx); err != nil {
			t.Fatalf("Run failed: %v", err)
		}

		repo.mu.Lock()
		defer repo.mu.Unlock()
		if len(repo.completeJobCalls) != 2 {
			t.Fatalf("expected 2 CompleteJob calls, got %d", len(repo.completeJobCalls))
		}
	})
}
