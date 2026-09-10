package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/dispatcher"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

// ExecutionError represents a structured, sanitized failure outcome from job execution.
type ExecutionError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *ExecutionError) Error() string {
	return fmt.Sprintf("[%s] %s (retryable=%t)", e.Code, e.Message, e.Retryable)
}

// JobExecutor executes a single durable job.
// Test fakes implement this interface to verify worker lifecycle behaviors.
type JobExecutor interface {
	Execute(ctx context.Context, job *model.Job) (any, error)
}

// DispatcherExecutor adapts the proxy Dispatcher to the JobExecutor interface.
// Delivery semantics are explicitly at-least-once.
type DispatcherExecutor struct {
	disp *dispatcher.Dispatcher
}

// NewDispatcherExecutor creates a new dispatcher-backed job executor.
func NewDispatcherExecutor(disp *dispatcher.Dispatcher) *DispatcherExecutor {
	return &DispatcherExecutor{disp: disp}
}

// Execute decodes Job.Request into model.TaskRequest, rejects streaming jobs,
// overwrites routing and task ID from trusted job columns, and invokes the dispatcher.
func (e *DispatcherExecutor) Execute(ctx context.Context, job *model.Job) (any, error) {
	if e == nil || e.disp == nil {
		return nil, &ExecutionError{
			Code:      "NIL_DISPATCHER",
			Message:   "dispatcher is not configured",
			Retryable: false,
		}
	}
	if job == nil {
		return nil, &ExecutionError{
			Code:      "INVALID_JOB",
			Message:   "job cannot be nil",
			Retryable: false,
		}
	}
	if len(job.Request) == 0 {
		return nil, &ExecutionError{
			Code:      "INVALID_PAYLOAD",
			Message:   "job request payload is empty",
			Retryable: false,
		}
	}

	var req model.TaskRequest
	if err := json.Unmarshal(job.Request, &req); err != nil {
		return nil, &ExecutionError{
			Code:      "INVALID_PAYLOAD",
			Message:   "failed to decode job request payload",
			Retryable: false,
		}
	}

	if req.Stream {
		return nil, &ExecutionError{
			Code:      "STREAMING_NOT_SUPPORTED",
			Message:   "streaming jobs are not supported in durable worker",
			Retryable: false,
		}
	}

	// Overwrite routing and task ID from trusted job columns
	req.ID = job.ID
	req.AgentID = job.AgentID
	req.RequiredCapability = job.RequiredCapability

	resp, err := e.disp.DispatchSingleAttempt(ctx, &req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, mapDispatcherError(err, job.MaxAttempts)
	}

	if resp != nil && resp.Status == model.StatusFailed {
		return nil, &ExecutionError{
			Code:      "TASK_FAILED",
			Message:   "downstream task reported failure",
			Retryable: false,
		}
	}

	return resp, nil
}

func mapDispatcherError(err error, maxAttempts int) *ExecutionError {
	var a2aErr *model.A2AError
	if errors.As(err, &a2aErr) {
		switch a2aErr.Code {
		case "DOWNSTREAM_TIMEOUT":
			return &ExecutionError{
				Code:      "DOWNSTREAM_TIMEOUT",
				Message:   "downstream agent request timed out",
				Retryable: maxAttempts > 1,
			}
		case "DOWNSTREAM_UNAVAILABLE":
			return &ExecutionError{
				Code:      "DOWNSTREAM_UNAVAILABLE",
				Message:   "downstream agent unavailable",
				Retryable: maxAttempts > 1,
			}
		case "AGENT_NOT_FOUND":
			return &ExecutionError{
				Code:      "AGENT_NOT_FOUND",
				Message:   "agent not found",
				Retryable: false,
			}
		case "CAPABILITY_NOT_FOUND":
			return &ExecutionError{
				Code:      "CAPABILITY_NOT_FOUND",
				Message:   "agent capability not found",
				Retryable: false,
			}
		case "CYCLE_DETECTED":
			return &ExecutionError{
				Code:      "CYCLE_DETECTED",
				Message:   "fallback cycle detected",
				Retryable: false,
			}
		case "INVALID_REQUEST":
			return &ExecutionError{
				Code:      "INVALID_REQUEST",
				Message:   "invalid task request",
				Retryable: false,
			}
		default:
			return &ExecutionError{
				Code:      "DOWNSTREAM_ERROR",
				Message:   "downstream execution failed",
				Retryable: false,
			}
		}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return &ExecutionError{
			Code:      "DOWNSTREAM_TIMEOUT",
			Message:   "downstream agent request timed out",
			Retryable: maxAttempts > 1,
		}
	}

	return &ExecutionError{
		Code:      "INTERNAL_ERROR",
		Message:   "internal execution failure",
		Retryable: false,
	}
}

// SanitizeFailure converts any execution error into bounded, sanitized failure code and message.
// Raw Go error text, URLs, credentials, response bodies, and panic values are never passed.
func SanitizeFailure(err error, maxAttempts int) (code, message string, retryable bool) {
	if err == nil {
		return "", "", false
	}

	var execErr *ExecutionError
	if errors.As(err, &execErr) {
		code = boundString(execErr.Code, model.MaxFailureCodeLength)
		message = boundString(execErr.Message, model.MaxFailureMessageLength)
		retryable = execErr.Retryable && (maxAttempts > 1)
		return code, message, retryable
	}

	mapped := mapDispatcherError(err, maxAttempts)
	return boundString(mapped.Code, model.MaxFailureCodeLength),
		boundString(mapped.Message, model.MaxFailureMessageLength),
		mapped.Retryable && (maxAttempts > 1)
}

func boundString(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes])
}
