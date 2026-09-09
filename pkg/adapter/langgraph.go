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

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/stream"
	"github.com/tidwall/gjson"
)

type LangGraphAdapter struct{}

func NewLangGraphAdapter() *LangGraphAdapter {
	return &LangGraphAdapter{}
}

func (a *LangGraphAdapter) Type() string {
	return "langgraph"
}

func (a *LangGraphAdapter) TranslateRequest(ctx context.Context, agent *config.AgentConfig, task *model.TaskRequest) (*http.Request, error) {
	if agent == nil || task == nil {
		return nil, fmt.Errorf("agent and task must not be nil")
	}

	threadID := task.ID
	if task.Context != nil {
		if tid, ok := task.Context["thread_id"].(string); ok && tid != "" {
			threadID = tid
		}
	}

	configurable := map[string]any{}
	if threadID != "" {
		configurable["thread_id"] = threadID
	}

	configMap := map[string]any{
		"configurable": configurable,
	}

	payload := map[string]any{
		"input":  task.Input,
		"config": configMap,
	}

	if agent.Options != nil {
		if assistantID, ok := agent.Options["assistant_id"]; ok {
			payload["assistant_id"] = assistantID
		}
		if optCfg, ok := agent.Options["config"].(map[string]any); ok {
			for k, v := range optCfg {
				if k == "configurable" {
					if optConf, isMap := v.(map[string]any); isMap {
						for ck, cv := range optConf {
							if ck == "thread_id" && threadID != "" {
								continue
							}
							configurable[ck] = cv
						}
						continue
					}
				}
				configMap[k] = v
			}
		}
		if streamMode, ok := agent.Options["stream_mode"]; ok {
			payload["stream_mode"] = streamMode
		}
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal langgraph request payload: %w", err)
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

func (a *LangGraphAdapter) TranslateResponse(ctx context.Context, agent *config.AgentConfig, resp *http.Response) (*model.TaskResponse, error) {
	if agent == nil {
		return nil, fmt.Errorf("agent must not be nil")
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("response or response body is nil")
	}
	defer func() { _ = resp.Body.Close() }()

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
		if gjson.Get(bodyStr, "detail").Exists() {
			errStr = gjson.Get(bodyStr, "detail").String()
		} else if gjson.Get(bodyStr, "error").Exists() {
			errStr = gjson.Get(bodyStr, "error").String()
		} else if gjson.Get(bodyStr, "message").Exists() {
			errStr = gjson.Get(bodyStr, "message").String()
		}
		taskResp.Error = &errStr
		return taskResp, nil
	}

	if gjson.Get(bodyStr, "output").Exists() {
		taskResp.Output = gjson.Get(bodyStr, "output").Value()
	} else if gjson.Get(bodyStr, "values").Exists() {
		taskResp.Output = gjson.Get(bodyStr, "values").Value()
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

func (a *LangGraphAdapter) TranslateStream(ctx context.Context, agent *config.AgentConfig, resp *http.Response, emitter stream.Emitter) error {
	if agent == nil {
		return fmt.Errorf("agent must not be nil")
	}
	if emitter == nil {
		return fmt.Errorf("emitter must not be nil")
	}
	if resp == nil || resp.Body == nil {
		return fmt.Errorf("response or response body is nil")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("upstream returned status %d: %s", resp.StatusCode, string(body))
		_ = emitter.Emit(model.EventTaskError, map[string]any{"error": errMsg, "agent_id": agent.ID})
		return fmt.Errorf("upstream returned status %d: %s", resp.StatusCode, string(body))
	}

	if err := emitter.Emit(model.EventTaskStarted, map[string]any{"agent_id": agent.ID}); err != nil {
		return err
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	currentEvent := ""
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			currentEvent = ""
			continue
		}

		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}

		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" || currentEvent == "end" {
				break
			}

			switch currentEvent {
			case "error":
				errMsg := data
				if gjson.Valid(data) {
					if gjson.Get(data, "message").Exists() {
						errMsg = gjson.Get(data, "message").String()
					} else if gjson.Get(data, "error").Exists() {
						errMsg = gjson.Get(data, "error").String()
					}
				}
				if errMsg == "" {
					errMsg = "unknown stream error"
				}
				_ = emitter.Emit(model.EventTaskError, map[string]any{"error": errMsg, "agent_id": agent.ID})
				return fmt.Errorf("upstream stream error: %s", errMsg)
			case "messages", "message", "token":
				var delta any = data
				if gjson.Valid(data) {
					if gjson.Get(data, "0.content").Exists() {
						delta = gjson.Get(data, "0.content").Value()
					} else if gjson.Get(data, "content").Exists() {
						delta = gjson.Get(data, "content").Value()
					} else {
						delta = gjson.Parse(data).Value()
					}
				}
				if err := emitter.Emit(model.EventTokenDelta, map[string]any{"delta": delta}); err != nil {
					return err
				}
			case "values", "updates", "steps", "step":
				var stepData any = data
				if gjson.Valid(data) {
					stepData = gjson.Parse(data).Value()
				}
				if err := emitter.Emit(model.EventStepProgress, map[string]any{"step": currentEvent, "data": stepData}); err != nil {
					return err
				}
			default:
				if gjson.Valid(data) && gjson.Get(data, "delta").Exists() {
					if err := emitter.Emit(model.EventTokenDelta, map[string]any{"delta": gjson.Get(data, "delta").Value()}); err != nil {
						return err
					}
				} else {
					var parsed any = data
					if gjson.Valid(data) {
						parsed = gjson.Parse(data).Value()
					}
					if err := emitter.Emit(model.EventStepProgress, map[string]any{"step": "progress", "data": parsed}); err != nil {
						return err
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return emitter.Emit(model.EventTaskCompleted, map[string]any{"status": model.StatusCompleted})
}
