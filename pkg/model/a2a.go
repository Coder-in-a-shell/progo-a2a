package model

import "time"

type TaskStatus string

const (
	StatusPending    TaskStatus = "PENDING"
	StatusInProgress TaskStatus = "IN_PROGRESS"
	StatusCompleted  TaskStatus = "COMPLETED"
	StatusFailed     TaskStatus = "FAILED"
	StatusCanceled   TaskStatus = "CANCELED"
)

type AgentCard struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
	Tags         []string `json:"tags,omitempty"`
}

type AgentListResponse struct {
	Agents []AgentCard `json:"agents"`
}

type TaskRequest struct {
	ID                 string         `json:"id,omitempty"`
	AgentID            string         `json:"agent_id,omitempty"`
	RequiredCapability string         `json:"required_capability,omitempty"`
	Input              any            `json:"input"`
	Context            map[string]any `json:"context,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
	Stream             bool           `json:"stream,omitempty"`
}

type Artifact struct {
	Name     string `json:"name"`
	MimeType string `json:"mime_type,omitempty"`
	URI      string `json:"uri,omitempty"`
	Data     any    `json:"data,omitempty"`
}

type TaskResponse struct {
	TaskID    string     `json:"task_id"`
	AgentID   string     `json:"agent_id"`
	Status    TaskStatus `json:"status"`
	Output    any        `json:"output,omitempty"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
	Error     *string    `json:"error,omitempty"`
	Timestamp time.Time  `json:"timestamp"`
}

type StreamEventType string

const (
	EventTaskStarted   StreamEventType = "task_started"
	EventStepProgress  StreamEventType = "step_progress"
	EventTokenDelta    StreamEventType = "token_delta"
	EventTaskCompleted StreamEventType = "task_completed"
	EventTaskError     StreamEventType = "task_error"
)

type StreamEvent struct {
	Type StreamEventType `json:"type"`
	Data any             `json:"data"`
}
