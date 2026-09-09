package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/model"
	"a2a-proxy/pkg/stream"
	"github.com/tidwall/gjson"
)

type AutoGenAdapter struct{}

func NewAutoGenAdapter() *AutoGenAdapter {
	return &AutoGenAdapter{}
}

func (a *AutoGenAdapter) Type() string {
	return "autogen"
}

func (a *AutoGenAdapter) TranslateRequest(ctx context.Context, agent *config.AgentConfig, task *model.TaskRequest) (*http.Request, error) {
	if agent == nil || task == nil {
		return nil, fmt.Errorf("agent and task must not be nil")
	}

	sender := "user"
	if agent.Options != nil {
		if s, ok := agent.Options["sender"].(string); ok && s != "" {
			sender = s
		}
	}
	if task.Context != nil {
		if s, ok := task.Context["sender"].(string); ok && s != "" {
			sender = s
		}
	}

	recipient := agent.ID
	if recipient == "" {
		recipient = agent.Name
	}
	if agent.Options != nil {
		if r, ok := agent.Options["recipient"].(string); ok && r != "" {
			recipient = r
		}
	}
	if task.Context != nil {
		if r, ok := task.Context["recipient"].(string); ok && r != "" {
			recipient = r
		}
	}

	payload := map[string]any{
		"sender":    sender,
		"recipient": recipient,
		"message":   task.Input,
	}

	if agent.Options != nil {
		for k, v := range agent.Options {
			if k != "sender" && k != "recipient" && k != "message" {
				payload[k] = v
			}
		}
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal autogen request payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, agent.Endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if task.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}

	applyOutboundAuth(req, agent.Auth)
	return req, nil
}

func (a *AutoGenAdapter) TranslateResponse(ctx context.Context, agent *config.AgentConfig, resp *http.Response) (*model.TaskResponse, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("response or response body is nil")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	taskResp := &model.TaskResponse{
		AgentID:   agent.ID,
		Timestamp: time.Now().UTC(),
		Status:    model.StatusCompleted,
	}

	bodyStr := string(body)
	if resp.StatusCode >= 400 {
		taskResp.Status = model.StatusFailed
		errStr := fmt.Sprintf("upstream returned status %d: %s", resp.StatusCode, bodyStr)
		if gjson.Get(bodyStr, "error").Exists() {
			errStr = gjson.Get(bodyStr, "error").String()
		} else if gjson.Get(bodyStr, "message").Exists() {
			errStr = gjson.Get(bodyStr, "message").String()
		}
		taskResp.Error = &errStr
		return taskResp, nil
	}

	if gjson.Get(bodyStr, "reply").Exists() {
		taskResp.Output = gjson.Get(bodyStr, "reply").Value()
	} else if gjson.Get(bodyStr, "response").Exists() {
		taskResp.Output = gjson.Get(bodyStr, "response").Value()
	} else if gjson.Get(bodyStr, "summary").Exists() {
		taskResp.Output = gjson.Get(bodyStr, "summary").Value()
	} else if gjson.Get(bodyStr, "message").Exists() {
		taskResp.Output = gjson.Get(bodyStr, "message").Value()
	} else if gjson.Get(bodyStr, "chat_history").Exists() && gjson.Get(bodyStr, "chat_history").IsArray() {
		arr := gjson.Get(bodyStr, "chat_history").Array()
		if len(arr) > 0 {
			last := arr[len(arr)-1]
			if last.Get("content").Exists() {
				taskResp.Output = last.Get("content").Value()
			} else {
				taskResp.Output = last.Value()
			}
		}
	} else {
		var parsed any
		if json.Unmarshal(body, &parsed) == nil {
			taskResp.Output = parsed
		} else {
			taskResp.Output = bodyStr
		}
	}

	return taskResp, nil
}

func (a *AutoGenAdapter) TranslateStream(ctx context.Context, agent *config.AgentConfig, resp *http.Response, emitter stream.Emitter) error {
	if resp == nil || resp.Body == nil {
		return fmt.Errorf("response or response body is nil")
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("upstream returned status %d: %s", resp.StatusCode, string(body))
		emitter.Emit(model.EventTaskError, map[string]any{"error": errMsg, "agent_id": agent.ID})
		return fmt.Errorf("upstream returned status %d: %s", resp.StatusCode, string(body))
	}

	if err := emitter.Emit(model.EventTaskStarted, map[string]any{"agent_id": agent.ID}); err != nil {
		return err
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}

			if gjson.Valid(data) {
				if gjson.Get(data, "step").Exists() {
					if err := emitter.Emit(model.EventStepProgress, map[string]any{"step": "step", "data": gjson.Parse(data).Value()}); err != nil {
						return err
					}
				} else if gjson.Get(data, "delta").Exists() {
					if err := emitter.Emit(model.EventTokenDelta, map[string]any{"delta": gjson.Get(data, "delta").Value()}); err != nil {
						return err
					}
				} else if gjson.Get(data, "content").Exists() {
					if err := emitter.Emit(model.EventTokenDelta, map[string]any{"delta": gjson.Get(data, "content").Value()}); err != nil {
						return err
					}
				} else if gjson.Get(data, "message").Exists() {
					if err := emitter.Emit(model.EventTokenDelta, map[string]any{"delta": gjson.Get(data, "message").Value()}); err != nil {
						return err
					}
				} else {
					if err := emitter.Emit(model.EventTokenDelta, map[string]any{"delta": gjson.Parse(data).Value()}); err != nil {
						return err
					}
				}
			} else {
				if err := emitter.Emit(model.EventTokenDelta, map[string]any{"delta": data}); err != nil {
					return err
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return emitter.Emit(model.EventTaskCompleted, map[string]any{"status": model.StatusCompleted})
}
