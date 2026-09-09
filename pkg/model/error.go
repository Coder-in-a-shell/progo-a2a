package model

import (
	"fmt"
	"time"
)

type A2AError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	AgentID   string         `json:"agent_id,omitempty"`
	Status    int            `json:"status"`
	Details   map[string]any `json:"details,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

func (e *A2AError) Error() string {
	return fmt.Sprintf("[%s] %s (agent=%s, status=%d)", e.Code, e.Message, e.AgentID, e.Status)
}

type A2AErrorEnvelope struct {
	Error A2AError `json:"error"`
}

func NewA2AError(code, message, agentID string, status int) *A2AErrorEnvelope {
	return &A2AErrorEnvelope{
		Error: A2AError{
			Code:      code,
			Message:   message,
			AgentID:   agentID,
			Status:    status,
			Timestamp: time.Now().UTC(),
		},
	}
}
