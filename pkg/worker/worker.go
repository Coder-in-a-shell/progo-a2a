package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/storage"
)

type jobKey struct {
	tenantID string
	jobID    string
}

// Engine orchestrates background execution of durable jobs.
// Delivery semantics are explicitly at-least-once.
type Engine struct {
	cfg      config.WorkerConfig
	repo     storage.JobRepository
	executor JobExecutor
	workerID string
	logger   *slog.Logger

	mu                sync.Mutex
	activeCancelFuncs map[jobKey]context.CancelFunc
	shuttingDown      bool

	// Testing hooks for deterministic synchronization in fakes
	onLeaseAcquired func(jobs []*model.Job)
	onJobRunning    func(job *model.Job)
	onJobCompleted  func(job *model.Job)
	onJobFailed     func(job *model.Job, retryable bool)
	onJobCanceled   func(job *model.Job)
}

// NewEngine creates a new durable worker engine.
func NewEngine(cfg config.WorkerConfig, repo storage.JobRepository, executor JobExecutor, opts ...Option) *Engine {
	workerID := cfg.WorkerID
	if workerID == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "worker"
		}
		workerID = fmt.Sprintf("%s-%d-%d", host, os.Getpid(), time.Now().UnixNano())
	}

	eng := &Engine{
		cfg:               cfg,
		repo:              repo,
		executor:          executor,
		workerID:          workerID,
		logger:            slog.Default(),
		activeCancelFuncs: make(map[jobKey]context.CancelFunc),
	}

	for _, opt := range opts {
		opt(eng)
	}

	return eng
}

// WorkerID returns the stable identifier used by this worker engine instance.
func (e *Engine) WorkerID() string {
	return e.workerID
}

// Run starts the worker poll, lease renewal, and reclamation loops.
// Run blocks until the provided context is canceled and all in-flight jobs are drained or timed out.
// Returns on parent cancellation even when no work exists.
func (e *Engine) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("worker context cannot be nil")
	}
	if e.repo == nil {
		return errors.New("job repository cannot be nil")
	}
	if e.executor == nil {
		return errors.New("job executor cannot be nil")
	}
	if e.cfg.Concurrency <= 0 {
		return errors.New("worker concurrency must be positive")
	}
	if e.cfg.Concurrency > storage.MaxJobBatchSize {
		return fmt.Errorf("worker concurrency must not exceed %d", storage.MaxJobBatchSize)
	}
	if e.cfg.BatchSize <= 0 {
		return errors.New("worker batch_size must be positive")
	}
	if e.cfg.BatchSize > storage.MaxJobBatchSize {
		return fmt.Errorf("worker batch_size must not exceed %d", storage.MaxJobBatchSize)
	}
	if e.cfg.BatchSize > e.cfg.Concurrency {
		return fmt.Errorf("worker batch_size (%d) must not exceed concurrency (%d)", e.cfg.BatchSize, e.cfg.Concurrency)
	}
	if e.cfg.PollIntervalMilliseconds <= 0 {
		return errors.New("worker poll_interval_milliseconds must be positive")
	}
	if e.cfg.PollIntervalMilliseconds > config.MaxWorkerPollIntervalMilliseconds {
		return fmt.Errorf("worker poll_interval_milliseconds must not exceed %d", config.MaxWorkerPollIntervalMilliseconds)
	}
	if e.cfg.LeaseDurationSeconds < 2 {
		return errors.New("worker lease_duration_seconds must be at least 2")
	}
	if e.cfg.LeaseDurationSeconds > int(storage.MaxJobLeaseDuration.Seconds()) {
		return fmt.Errorf("worker lease_duration_seconds must not exceed %d", int(storage.MaxJobLeaseDuration.Seconds()))
	}
	if e.cfg.RenewalIntervalSeconds <= 0 {
		return errors.New("worker renewal_interval_seconds must be positive")
	}
	if e.cfg.RenewalIntervalSeconds > e.cfg.LeaseDurationSeconds/2 {
		return fmt.Errorf("worker renewal_interval_seconds (%d) must be at most half of lease_duration_seconds (%d)", e.cfg.RenewalIntervalSeconds, e.cfg.LeaseDurationSeconds)
	}
	if e.cfg.RetryBackoffSeconds <= 0 {
		return errors.New("worker retry_backoff_seconds must be positive")
	}
	if e.cfg.RetryBackoffSeconds > int(model.MaxRetryBackoff.Seconds()) {
		return fmt.Errorf("worker retry_backoff_seconds must not exceed %d", int(model.MaxRetryBackoff.Seconds()))
	}
	if e.cfg.DrainTimeoutSeconds <= 0 {
		return errors.New("worker drain_timeout_seconds must be positive")
	}
	if e.cfg.DrainTimeoutSeconds > config.MaxWorkerDrainTimeoutSeconds {
		return fmt.Errorf("worker drain_timeout_seconds must not exceed %d", config.MaxWorkerDrainTimeoutSeconds)
	}
	if strings.TrimSpace(e.workerID) == "" {
		return errors.New("worker ID cannot be blank")
	}
	if utf8.RuneCountInString(e.workerID) > model.MaxLeaseOwnerLength {
		return fmt.Errorf("worker ID must not exceed %d characters", model.MaxLeaseOwnerLength)
	}

	e.logger.Info("starting durable worker engine",
		"worker_id", e.workerID,
		"concurrency", e.cfg.Concurrency,
		"batch_size", e.cfg.BatchSize,
		"poll_interval_ms", e.cfg.PollIntervalMilliseconds,
		"lease_duration_s", e.cfg.LeaseDurationSeconds,
		"renewal_interval_s", e.cfg.RenewalIntervalSeconds,
		"drain_timeout_s", e.cfg.DrainTimeoutSeconds,
	)

	// Capacity semaphore: tokens represent available concurrent job slots
	capacityTokens := make(chan struct{}, e.cfg.Concurrency)
	for i := 0; i < e.cfg.Concurrency; i++ {
		capacityTokens <- struct{}{}
	}

	var activeWg sync.WaitGroup
	var loopsWg sync.WaitGroup

	// Background lease reclamation loop
	loopsWg.Add(1)
	go func() {
		defer loopsWg.Done()
		e.runReclamationLoop(ctx)
	}()

	// Polling and execution loop
	pollTimer := time.NewTimer(0)
	if !pollTimer.Stop() {
		select {
		case <-pollTimer.C:
		default:
		}
	}

	pollInterval := e.cfg.PollInterval()
	if pollInterval <= 0 {
		pollInterval = time.Second
	}

pollLoop:
	for {
		if ctx.Err() != nil {
			break pollLoop
		}

		// Calculate available capacity up to batch_size
		var acquiredTokens int
		for acquiredTokens < e.cfg.BatchSize {
			select {
			case <-capacityTokens:
				acquiredTokens++
			default:
				goto capacityEvaluated
			}
		}

	capacityEvaluated:
		if acquiredTokens == 0 {
			// No capacity available: wait for a token, poll timer, or shutdown
			select {
			case <-ctx.Done():
				break pollLoop
			case <-capacityTokens:
				// At least 1 token is available now
				acquiredTokens = 1
				for acquiredTokens < e.cfg.BatchSize {
					select {
					case <-capacityTokens:
						acquiredTokens++
					default:
						goto hasTokens
					}
				}
			}
		}

	hasTokens:
		if ctx.Err() != nil {
			// Return unused tokens before exiting
			for i := 0; i < acquiredTokens; i++ {
				capacityTokens <- struct{}{}
			}
			break pollLoop
		}

		// Acquire leases with database time and fencing
		jobs, err := e.repo.AcquireLeases(ctx, e.workerID, acquiredTokens, e.cfg.LeaseDuration())
		if err != nil {
			// Return all tokens
			for i := 0; i < acquiredTokens; i++ {
				capacityTokens <- struct{}{}
			}
			if ctx.Err() != nil {
				break pollLoop
			}
			e.logger.Warn("transient error acquiring job leases", "error", err)
			// Bounded wait on error to avoid busy loop
			pollTimer.Reset(pollInterval)
			select {
			case <-ctx.Done():
				pollTimer.Stop()
				break pollLoop
			case <-pollTimer.C:
			}
			continue
		}

		// Close the lease-after-shutdown race: if parent cancellation occurred while
		// AcquireLeases was in flight, do not start them. Return tokens and break immediately.
		if ctx.Err() != nil {
			for i := 0; i < acquiredTokens; i++ {
				capacityTokens <- struct{}{}
			}
			e.logger.Warn("shutdown signaled while acquiring leases; discarding acquired jobs for reclamation", "count", len(jobs))
			break pollLoop
		}

		if len(jobs) == 0 {
			// No jobs available: return all tokens and wait poll interval
			for i := 0; i < acquiredTokens; i++ {
				capacityTokens <- struct{}{}
			}
			pollTimer.Reset(pollInterval)
			select {
			case <-ctx.Done():
				pollTimer.Stop()
				break pollLoop
			case <-pollTimer.C:
			}
			continue
		}

		// Guard against rogue repository returning more jobs than requested
		if len(jobs) > acquiredTokens {
			e.logger.Error("repository returned more jobs than requested; truncating batch", "requested", acquiredTokens, "got", len(jobs))
			jobs = jobs[:acquiredTokens]
		}

		// Return unused tokens if acquired fewer jobs than requested
		unusedTokens := acquiredTokens - len(jobs)
		for i := 0; i < unusedTokens; i++ {
			capacityTokens <- struct{}{}
		}

		// Discard nil jobs safely, returning their token to the capacity semaphore
		validJobs := make([]*model.Job, 0, len(jobs))
		for _, job := range jobs {
			if job == nil {
				e.logger.Error("repository returned nil job; discarding slot")
				capacityTokens <- struct{}{}
				continue
			}
			validJobs = append(validJobs, job)
		}

		if len(validJobs) == 0 {
			continue
		}

		if e.onLeaseAcquired != nil {
			e.onLeaseAcquired(validJobs)
		}

		// Dispatch jobs concurrently
		for _, job := range validJobs {
			activeWg.Add(1)
			go func(j *model.Job) {
				defer func() {
					capacityTokens <- struct{}{}
					activeWg.Done()
				}()
				e.processJob(j)
			}(job)
		}
	}

	pollTimer.Stop()

	// Shutdown phase 1: stop acquiring new leases
	e.mu.Lock()
	e.shuttingDown = true
	e.mu.Unlock()
	e.logger.Info("shutdown initiated: stopped acquiring leases, draining active jobs", "drain_timeout", e.cfg.DrainTimeout().String())

	// Shutdown phase 2: allow active jobs to drain while leases continue renewing
	drainDone := make(chan struct{})
	go func() {
		activeWg.Wait()
		close(drainDone)
	}()

	drainTimer := time.NewTimer(e.cfg.DrainTimeout())
	defer drainTimer.Stop()

	select {
	case <-drainDone:
		e.logger.Info("all active jobs drained successfully")
	case <-drainTimer.C:
		e.logger.Warn("drain deadline reached; canceling remaining active jobs")
		e.mu.Lock()
		for _, cancel := range e.activeCancelFuncs {
			cancel()
		}
		e.mu.Unlock()
		// Engine returns at deadline without unbounded wait on drainDone
	}

	// Wait for background loops (reclamation) to finish
	loopsWg.Wait()

	e.logger.Info("durable worker engine stopped cleanly")
	return nil
}

func (e *Engine) runReclamationLoop(ctx context.Context) {
	// Reclamation cadence must remain frequent even when job retry backoff is
	// intentionally long. The renewal interval is bounded to at most half the
	// lease duration, so it is also a safe expired-lease sweep cadence.
	reclaimInterval := e.cfg.RenewalInterval()
	if reclaimInterval <= 0 {
		reclaimInterval = 15 * time.Second
	}

	// Initial sweep on startup
	e.reclaimOnce(ctx)

	ticker := time.NewTicker(reclaimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.reclaimOnce(ctx)
		}
	}
}

func (e *Engine) reclaimOnce(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	reclaimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	n, err := e.repo.ReclaimExpiredLeases(reclaimCtx, e.cfg.BatchSize, e.cfg.RetryBackoff())
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		e.logger.Warn("transient error reclaiming expired leases", "error", err)
		return
	}
	if n > 0 {
		e.logger.Info("reclaimed expired leases", "count", n)
	}
}

func (e *Engine) processJob(job *model.Job) {
	if job == nil {
		return
	}
	k := jobKey{tenantID: job.TenantID, jobID: job.ID}
	jobCtx, cancelJob := context.WithCancel(context.Background())
	defer cancelJob()

	e.mu.Lock()
	if e.shuttingDown {
		// Shutdown started between lease acquisition and processing
		e.mu.Unlock()
		return
	}
	e.activeCancelFuncs[k] = cancelJob
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		delete(e.activeCancelFuncs, k)
		e.mu.Unlock()
	}()

	fence := job.Fence()

	// 1. Mark running before execution
	if err := e.repo.MarkRunning(jobCtx, fence); err != nil {
		if errors.Is(err, storage.ErrCancellationRequested) {
			e.logger.Info("cancellation requested before running; acknowledging", "job_id", job.ID)
			ackCtx, ackCancel := context.WithTimeout(context.Background(), 5*time.Second)
			ackErr := e.repo.AcknowledgeCancellation(ackCtx, fence)
			ackCancel()
			if ackErr != nil {
				if errors.Is(ackErr, storage.ErrLeaseLost) || errors.Is(ackErr, storage.ErrJobNotFound) {
					e.logger.Warn("lease lost or job not found acknowledging cancellation before start", "job_id", job.ID, "error", ackErr)
				} else {
					e.logger.Error("transient error acknowledging cancellation before start", "job_id", job.ID, "error", ackErr)
				}
				// Once cancellation intent is known, never permit downstream execution
				return
			}
			if e.onJobCanceled != nil {
				e.onJobCanceled(job)
			}
			return
		}
		if errors.Is(err, storage.ErrLeaseLost) || errors.Is(err, storage.ErrJobNotFound) {
			e.logger.Warn("lease lost before running; skipping execution", "job_id", job.ID, "error", err)
			return
		}
		e.logger.Error("failed to mark running due to storage error", "job_id", job.ID, "error", err)
		return
	}

	if e.onJobRunning != nil {
		e.onJobRunning(job)
	}

	// 2. Start lease renewal goroutine
	renewalCtx, stopRenewal := context.WithCancel(context.Background())
	defer stopRenewal()

	renewalDone := make(chan struct{})
	go func() {
		defer close(renewalDone)
		e.runLeaseRenewal(renewalCtx, jobCtx, job, cancelJob)
	}()

	// 3. Execute with panic recovery
	var (
		resp     any
		execErr  error
		panicked bool
	)

	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		resp, execErr = e.executor.Execute(jobCtx, job)
	}()

	// Stop renewal goroutine before writing terminal state
	stopRenewal()
	<-renewalDone

	// Jobs that finish inside the graceful drain window still persist their
	// outcome. Only forced drain cancellation, lease loss, or job cancellation
	// cancels jobCtx and suppresses terminal writes.
	if jobCtx.Err() != nil {
		e.logger.Info("job execution canceled by shutdown or context; skipping state persistence", "job_id", job.ID)
		return
	}

	// 4. Handle execution outcome
	if panicked {
		// Never log a recovered panic value to prevent secret or request data leaks
		e.logger.Error("job execution panicked; recovering generic failure", "job_id", job.ID)
		failCtx, failCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer failCancel()

		failErr := e.repo.FailJob(failCtx, model.FailJobInput{
			TenantID:       job.TenantID,
			JobID:          job.ID,
			LeaseOwner:     fence.LeaseOwner,
			LeaseToken:     fence.LeaseToken,
			FailureCode:    "PANIC",
			FailureMessage: "internal execution error occurred",
			Retryable:      false,
		})
		if failErr != nil {
			e.logger.Warn("failed to persist panic failure", "job_id", job.ID, "error", failErr)
		}
		if e.onJobFailed != nil {
			e.onJobFailed(job, false)
		}
		return
	}

	if execErr != nil {
		code, message, retryable := SanitizeFailure(execErr, job.MaxAttempts)
		failCtx, failCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer failCancel()

		failErr := e.repo.FailJob(failCtx, model.FailJobInput{
			TenantID:       job.TenantID,
			JobID:          job.ID,
			LeaseOwner:     fence.LeaseOwner,
			LeaseToken:     fence.LeaseToken,
			FailureCode:    code,
			FailureMessage: message,
			Retryable:      retryable,
			Backoff:        e.cfg.RetryBackoff(),
		})
		if failErr != nil {
			e.logger.Warn("failed to persist job failure", "job_id", job.ID, "error", failErr)
		}

		if e.onJobFailed != nil {
			e.onJobFailed(job, retryable)
		}
		return
	}

	// Execution succeeded
	compCtx, compCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer compCancel()

	if compErr := e.repo.CompleteJob(compCtx, fence, resp); compErr != nil {
		e.logger.Warn("failed to complete job", "job_id", job.ID, "error", compErr)
		return
	}

	if e.onJobCompleted != nil {
		e.onJobCompleted(job)
	}
}

func (e *Engine) runLeaseRenewal(renewalCtx, jobCtx context.Context, job *model.Job, cancelJob context.CancelFunc) {
	interval := e.cfg.RenewalInterval()
	if interval <= 0 {
		interval = 10 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	fence := job.Fence()

	for {
		select {
		case <-renewalCtx.Done():
			return
		case <-jobCtx.Done():
			return
		case <-ticker.C:
			// 1. Detect cancel_requested_at during renewal
			checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
			snap, err := e.repo.GetJob(checkCtx, job.TenantID, job.ID)
			checkCancel()

			if err != nil {
				if errors.Is(err, storage.ErrJobNotFound) {
					e.logger.Warn("job not found during renewal check; canceling job context", "job_id", job.ID)
					cancelJob()
					return
				}
				e.logger.Warn("transient error checking job cancellation intent", "job_id", job.ID, "error", err)
			} else if snap != nil && snap.CancelRequestedAt != nil {
				e.logger.Info("cancellation intent detected during renewal; acknowledging", "job_id", job.ID)
				cancelJob() // Stop downstream execution immediately

				ackCtx, ackCancel := context.WithTimeout(context.Background(), 5*time.Second)
				ackErr := e.repo.AcknowledgeCancellation(ackCtx, fence)
				ackCancel()
				if ackErr != nil {
					if errors.Is(ackErr, storage.ErrLeaseLost) || errors.Is(ackErr, storage.ErrJobNotFound) {
						e.logger.Warn("lease lost or job not found acknowledging cancellation during renewal", "job_id", job.ID, "error", ackErr)
					} else {
						e.logger.Error("transient error acknowledging cancellation during renewal", "job_id", job.ID, "error", ackErr)
					}
					return
				}
				if e.onJobCanceled != nil {
					e.onJobCanceled(job)
				}
				return
			}

			// 2. Renew active lease
			renewCtx, renewCancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = e.repo.RenewLease(renewCtx, fence, e.cfg.LeaseDuration())
			renewCancel()

			if err != nil {
				if errors.Is(err, storage.ErrLeaseLost) || errors.Is(err, storage.ErrJobNotFound) {
					e.logger.Warn("lease lost during renewal; canceling execution context immediately", "job_id", job.ID, "error", err)
					cancelJob()
					return
				}
				e.logger.Warn("transient error renewing lease", "job_id", job.ID, "error", err)
			}
		}
	}
}
