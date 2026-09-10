package dispatcher

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/adapter"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/metrics"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/stream"
)

type trackingEmitter struct {
	stream.Emitter
	hasEmitted bool
}

func (t *trackingEmitter) Emit(eventType model.StreamEventType, data any) error {
	t.hasEmitted = true
	return t.Emitter.Emit(eventType, data)
}

// Dispatcher orchestrates routing, retries, and fallback failovers for agents.
type Dispatcher struct {
	cfg             *config.Config
	reg             *adapter.Registry
	client          *http.Client
	backoff         *BackoffPolicy
	metricsRegistry *metrics.Registry
}

// New creates a new Dispatcher with connection-pooled HTTP client and default exponential backoff.
func New(cfg *config.Config, reg *adapter.Registry) *Dispatcher {
	var transport *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = dt.Clone()
	} else {
		transport = &http.Transport{}
	}
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 100
	transport.IdleConnTimeout = 90 * time.Second

	return &Dispatcher{
		cfg: cfg,
		reg: reg,
		client: &http.Client{
			Transport: transport,
		},
		backoff: DefaultBackoffPolicy(),
	}
}

// SetHTTPClient overrides the internal http.Client (e.g. for testing).
func (d *Dispatcher) SetHTTPClient(client *http.Client) {
	d.client = client
}

// SetBackoffPolicy overrides the internal retry backoff policy (e.g. for testing).
func (d *Dispatcher) SetBackoffPolicy(b *BackoffPolicy) {
	d.backoff = b
}

// SetMetricsRegistry configures the metrics registry for tracking retries and fallbacks.
func (d *Dispatcher) SetMetricsRegistry(reg *metrics.Registry) {
	d.metricsRegistry = reg
}

// Registry returns the adapter registry used by the dispatcher.
func (d *Dispatcher) Registry() *adapter.Registry {
	return d.reg
}

// Config returns the configuration used by the dispatcher.
func (d *Dispatcher) Config() *config.Config {
	return d.cfg
}

// GetAgent returns the AgentConfig for the specified agent ID or an error if not found.
func (d *Dispatcher) GetAgent(agentID string) (*config.AgentConfig, error) {
	if d.cfg != nil {
		for i := range d.cfg.Agents {
			if d.cfg.Agents[i].ID == agentID {
				return &d.cfg.Agents[i], nil
			}
		}
	}
	return nil, &model.A2AError{
		Code:      "AGENT_NOT_FOUND",
		Message:   fmt.Sprintf("agent '%s' not found", agentID),
		AgentID:   agentID,
		Status:    http.StatusNotFound,
		Timestamp: time.Now().UTC(),
	}
}

// FindAgentByCapability returns the first AgentConfig that supports the given capability.
func (d *Dispatcher) FindAgentByCapability(capability string) (*config.AgentConfig, error) {
	if d.cfg != nil {
		targetCap := strings.ToLower(strings.TrimSpace(capability))
		for i := range d.cfg.Agents {
			for _, capStr := range d.cfg.Agents[i].Capabilities {
				if strings.EqualFold(strings.TrimSpace(capStr), targetCap) {
					return &d.cfg.Agents[i], nil
				}
			}
		}
	}
	return nil, &model.A2AError{
		Code:      "CAPABILITY_NOT_FOUND",
		Message:   fmt.Sprintf("no agent found with capability '%s'", capability),
		Status:    http.StatusNotFound,
		Timestamp: time.Now().UTC(),
	}
}

// Dispatch executes a task synchronously with per-agent timeout, exponential backoff retries,
// and automatic fallback cascading.
func (d *Dispatcher) Dispatch(ctx context.Context, task *model.TaskRequest) (*model.TaskResponse, error) {
	targetAgent, err := d.resolveAgent(task)
	if err != nil {
		return nil, err
	}
	visited := make(map[string]bool)
	return d.dispatchWithFallback(ctx, task, targetAgent, visited)
}

// DispatchSingleAttempt executes exactly one downstream request with no
// dispatcher retry or fallback. Durable workers use this entry point so the
// job ledger is the sole authority for retry attempts and backoff.
func (d *Dispatcher) DispatchSingleAttempt(ctx context.Context, task *model.TaskRequest) (*model.TaskResponse, error) {
	targetAgent, err := d.resolveAgent(task)
	if err != nil {
		return nil, err
	}
	agent := *targetAgent
	agent.Retries = 0
	agent.FallbackAgentIDs = nil

	resp, err := d.invokeAgentWithRetries(ctx, task, &agent)
	if err == nil {
		return resp, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") {
		return nil, &model.A2AError{
			Code:      "DOWNSTREAM_TIMEOUT",
			Message:   "Agent timed out",
			AgentID:   agent.ID,
			Status:    http.StatusGatewayTimeout,
			Timestamp: time.Now().UTC(),
		}
	}
	return nil, &model.A2AError{
		Code:      "DOWNSTREAM_UNAVAILABLE",
		Message:   "Downstream agent invocation failed",
		AgentID:   agent.ID,
		Status:    http.StatusBadGateway,
		Timestamp: time.Now().UTC(),
	}
}

// DispatchStream executes a streaming task with per-agent timeout, exponential backoff retries,
// and automatic fallback cascading before the stream is established.
func (d *Dispatcher) DispatchStream(ctx context.Context, task *model.TaskRequest, emitter stream.Emitter) error {
	targetAgent, err := d.resolveAgent(task)
	if err != nil {
		return err
	}
	visited := make(map[string]bool)
	return d.dispatchStreamWithFallback(ctx, task, targetAgent, emitter, visited)
}

func (d *Dispatcher) resolveAgent(task *model.TaskRequest) (*config.AgentConfig, error) {
	if task == nil {
		return nil, &model.A2AError{
			Code:      "INVALID_REQUEST",
			Message:   "task request must not be nil",
			Status:    http.StatusBadRequest,
			Timestamp: time.Now().UTC(),
		}
	}
	if task.AgentID != "" {
		return d.GetAgent(task.AgentID)
	}
	if task.RequiredCapability != "" {
		return d.FindAgentByCapability(task.RequiredCapability)
	}
	return nil, &model.A2AError{
		Code:      "INVALID_REQUEST",
		Message:   "either agent_id or required_capability must be specified",
		Status:    http.StatusBadRequest,
		Timestamp: time.Now().UTC(),
	}
}

func (d *Dispatcher) dispatchWithFallback(ctx context.Context, task *model.TaskRequest, agent *config.AgentConfig, visited map[string]bool) (*model.TaskResponse, error) {
	if visited[agent.ID] {
		return nil, &model.A2AError{
			Code:      "CYCLE_DETECTED",
			Message:   fmt.Sprintf("fallback cycle detected for agent %s", agent.ID),
			AgentID:   agent.ID,
			Status:    http.StatusBadGateway,
			Timestamp: time.Now().UTC(),
		}
	}
	visited[agent.ID] = true

	resp, err := d.invokeAgentWithRetries(ctx, task, agent)
	if err == nil {
		return resp, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Cascade to fallback agents in order
	for _, fallbackID := range agent.FallbackAgentIDs {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if visited[fallbackID] {
			continue
		}
		fallbackAgent, getErr := d.GetAgent(fallbackID)
		if getErr != nil {
			continue
		}
		slog.Info("failing over to fallback agent",
			"from_agent_id", agent.ID,
			"to_agent_id", fallbackID,
		)
		if d.metricsRegistry != nil {
			d.metricsRegistry.IncFallbackTriggered(agent.ID, fallbackID)
		}
		fbResp, fbErr := d.dispatchWithFallback(ctx, task, fallbackAgent, visited)
		if fbErr == nil {
			return fbResp, nil
		}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") {
		return nil, &model.A2AError{
			Code:      "DOWNSTREAM_TIMEOUT",
			Message:   fmt.Sprintf("Agent timed out: %v", err),
			AgentID:   agent.ID,
			Status:    http.StatusGatewayTimeout,
			Timestamp: time.Now().UTC(),
		}
	}

	return nil, &model.A2AError{
		Code:      "DOWNSTREAM_UNAVAILABLE",
		Message:   "All attempts to invoke downstream agents failed",
		AgentID:   agent.ID,
		Status:    http.StatusBadGateway,
		Timestamp: time.Now().UTC(),
	}
}

func (d *Dispatcher) invokeAgentWithRetries(ctx context.Context, task *model.TaskRequest, agent *config.AgentConfig) (*model.TaskResponse, error) {
	ad, err := d.reg.Get(agent.Type)
	if err != nil {
		return nil, fmt.Errorf("adapter error for agent %s: %w", agent.ID, err)
	}

	maxRetries := agent.Retries
	if maxRetries < 0 {
		maxRetries = 0
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if d.metricsRegistry != nil {
				d.metricsRegistry.IncRetries(agent.ID)
			}
			slog.Warn("retrying agent invocation",
				"agent_id", agent.ID,
				"attempt", attempt,
				"max_retries", maxRetries,
				"error", lastErr,
			)
			if sleepErr := d.backoff.Sleep(ctx, attempt-1); sleepErr != nil {
				return nil, sleepErr
			}
		}

		attemptCtx := ctx
		var cancel context.CancelFunc
		if agent.TimeoutSeconds > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, time.Duration(agent.TimeoutSeconds)*time.Second)
		}

		req, err := ad.TranslateRequest(attemptCtx, agent, task)
		if err != nil {
			if cancel != nil {
				cancel()
			}
			return nil, fmt.Errorf("failed to translate request: %w", err)
		}

		resp, err := d.client.Do(req)
		if err != nil {
			if cancel != nil {
				cancel()
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}

		if resp.StatusCode >= 500 {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if cancel != nil {
				cancel()
			}
			lastErr = fmt.Errorf("downstream returned status %d", resp.StatusCode)
			continue
		}

		taskResp, err := ad.TranslateResponse(attemptCtx, agent, resp)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			return nil, fmt.Errorf("failed to translate response: %w", err)
		}

		if taskResp.TaskID == "" {
			if task.ID != "" {
				taskResp.TaskID = task.ID
			} else {
				taskResp.TaskID = generateTaskID()
			}
		}
		if taskResp.AgentID == "" {
			taskResp.AgentID = agent.ID
		}

		return taskResp, nil
	}

	return nil, lastErr
}

func (d *Dispatcher) dispatchStreamWithFallback(ctx context.Context, task *model.TaskRequest, agent *config.AgentConfig, emitter stream.Emitter, visited map[string]bool) error {
	if visited[agent.ID] {
		return &model.A2AError{
			Code:      "CYCLE_DETECTED",
			Message:   fmt.Sprintf("fallback cycle detected for agent %s", agent.ID),
			AgentID:   agent.ID,
			Status:    http.StatusBadGateway,
			Timestamp: time.Now().UTC(),
		}
	}
	visited[agent.ID] = true

	tracker, ok := emitter.(*trackingEmitter)
	if !ok {
		tracker = &trackingEmitter{Emitter: emitter}
	}

	err := d.invokeAgentStreamWithRetries(ctx, task, agent, tracker)
	if err == nil {
		return nil
	}

	if tracker.hasEmitted {
		return err
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Cascade to fallback agents
	for _, fallbackID := range agent.FallbackAgentIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if tracker.hasEmitted {
			return err
		}
		if visited[fallbackID] {
			continue
		}
		fallbackAgent, getErr := d.GetAgent(fallbackID)
		if getErr != nil {
			continue
		}
		slog.Info("failing over to streaming fallback agent",
			"from_agent_id", agent.ID,
			"to_agent_id", fallbackID,
		)
		if d.metricsRegistry != nil {
			d.metricsRegistry.IncFallbackTriggered(agent.ID, fallbackID)
		}
		fbErr := d.dispatchStreamWithFallback(ctx, task, fallbackAgent, tracker, visited)
		if fbErr == nil {
			return nil
		}
		if tracker.hasEmitted {
			return fbErr
		}
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") {
		return &model.A2AError{
			Code:      "DOWNSTREAM_TIMEOUT",
			Message:   fmt.Sprintf("Agent timed out: %v", err),
			AgentID:   agent.ID,
			Status:    http.StatusGatewayTimeout,
			Timestamp: time.Now().UTC(),
		}
	}

	return &model.A2AError{
		Code:      "DOWNSTREAM_UNAVAILABLE",
		Message:   "All attempts to invoke downstream agents failed",
		AgentID:   agent.ID,
		Status:    http.StatusBadGateway,
		Timestamp: time.Now().UTC(),
	}
}

func (d *Dispatcher) invokeAgentStreamWithRetries(ctx context.Context, task *model.TaskRequest, agent *config.AgentConfig, emitter stream.Emitter) error {
	ad, err := d.reg.Get(agent.Type)
	if err != nil {
		return fmt.Errorf("adapter error for agent %s: %w", agent.ID, err)
	}

	maxRetries := agent.Retries
	if maxRetries < 0 {
		maxRetries = 0
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if d.metricsRegistry != nil {
				d.metricsRegistry.IncRetries(agent.ID)
			}
			slog.Warn("retrying streaming agent invocation",
				"agent_id", agent.ID,
				"attempt", attempt,
				"max_retries", maxRetries,
				"error", lastErr,
			)
			if sleepErr := d.backoff.Sleep(ctx, attempt-1); sleepErr != nil {
				return sleepErr
			}
		}

		attemptCtx := ctx
		var cancel context.CancelFunc
		if agent.TimeoutSeconds > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, time.Duration(agent.TimeoutSeconds)*time.Second)
		}

		streamTask := *task
		streamTask.Stream = true

		req, err := ad.TranslateRequest(attemptCtx, agent, &streamTask)
		if err != nil {
			if cancel != nil {
				cancel()
			}
			return fmt.Errorf("failed to translate stream request: %w", err)
		}

		resp, err := d.client.Do(req)
		if err != nil {
			if cancel != nil {
				cancel()
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = err
			continue
		}

		if resp.StatusCode >= 500 {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if cancel != nil {
				cancel()
			}
			lastErr = fmt.Errorf("downstream returned status %d", resp.StatusCode)
			continue
		}

		err = ad.TranslateStream(attemptCtx, agent, resp, emitter)
		if cancel != nil {
			cancel()
		}
		return err
	}

	return lastErr
}

func generateTaskID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("task-%x", b)
}
