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

type OpenAIAdapter struct{}

func NewOpenAIAdapter() *OpenAIAdapter {
	return &OpenAIAdapter{}
}

func (a *OpenAIAdapter) Type() string {
	return "openai"
}

func (a *OpenAIAdapter) TranslateRequest(ctx context.Context, agent *config.AgentConfig, task *model.TaskRequest) (*http.Request, error) {
	if agent == nil || task == nil {
		return nil, fmt.Errorf("agent and task must not be nil")
	}

	modelName := "gpt-4o"
	if agent.Options != nil {
		if m, ok := agent.Options["model"].(string); ok && m != "" {
			modelName = m
		}
	}

	var messages []any
	switch v := task.Input.(type) {
	case []any:
		messages = v
	case []map[string]any:
		for _, item := range v {
			messages = append(messages, item)
		}
	case map[string]any:
		if rawMsgs, ok := v["messages"].([]any); ok {
			messages = rawMsgs
		} else if rawMsgs, ok := v["messages"].([]map[string]any); ok {
			for _, item := range rawMsgs {
				messages = append(messages, item)
			}
		} else {
			messages = []any{map[string]any{"role": "user", "content": v}}
		}
	default:
		messages = []any{map[string]any{"role": "user", "content": task.Input}}
	}

	var systemPrompt string
	if agent.Options != nil {
		if sp, ok := agent.Options["system_prompt"].(string); ok && sp != "" {
			systemPrompt = sp
		}
	}
	if task.Context != nil {
		if sp, ok := task.Context["system"].(string); ok && sp != "" {
			systemPrompt = sp
		}
	}

	if systemPrompt != "" {
		messages = append([]any{map[string]any{"role": "system", "content": systemPrompt}}, messages...)
	}

	payload := map[string]any{
		"model":    modelName,
		"messages": messages,
	}

	if task.Stream {
		payload["stream"] = true
	}

	if agent.Options != nil {
		for k, v := range agent.Options {
			if k != "model" && k != "system_prompt" && k != "messages" && k != "stream" {
				payload[k] = v
			}
		}
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal openai request payload: %w", err)
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

func (a *OpenAIAdapter) TranslateResponse(ctx context.Context, agent *config.AgentConfig, resp *http.Response) (*model.TaskResponse, error) {
	if agent == nil {
		return nil, fmt.Errorf("agent must not be nil")
	}
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
		if gjson.Get(bodyStr, "error.message").Exists() {
			errStr = gjson.Get(bodyStr, "error.message").String()
		} else if gjson.Get(bodyStr, "message").Exists() {
			errStr = gjson.Get(bodyStr, "message").String()
		}
		taskResp.Error = &errStr
		return taskResp, nil
	}

	val := gjson.Get(bodyStr, "choices.0.message.content")
	toolCalls := gjson.Get(bodyStr, "choices.0.message.tool_calls")
	if val.Exists() && val.Type != gjson.Null && val.String() != "" {
		taskResp.Output = val.String()
	} else if toolCalls.Exists() && toolCalls.Type != gjson.Null {
		taskResp.Output = toolCalls.Value()
	} else if txt := gjson.Get(bodyStr, "choices.0.text"); txt.Exists() && txt.Type != gjson.Null && txt.String() != "" {
		taskResp.Output = txt.String()
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

func (a *OpenAIAdapter) TranslateStream(ctx context.Context, agent *config.AgentConfig, resp *http.Response, emitter stream.Emitter) error {
	if agent == nil {
		return fmt.Errorf("agent must not be nil")
	}
	if emitter == nil {
		return fmt.Errorf("emitter must not be nil")
	}
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

			if errVal := gjson.Get(data, "error"); errVal.Exists() {
				errMsg := errVal.String()
				if msg := errVal.Get("message"); msg.Exists() && msg.String() != "" {
					errMsg = msg.String()
				}
				if errMsg == "" {
					errMsg = "unknown stream error"
				}
				emitter.Emit(model.EventTaskError, map[string]any{"error": errMsg, "agent_id": agent.ID})
				return fmt.Errorf("upstream stream error: %s", errMsg)
			}

			delta := gjson.Get(data, "choices.0.delta.content")
			if delta.Exists() && delta.String() != "" {
				if err := emitter.Emit(model.EventTokenDelta, map[string]any{"delta": delta.String()}); err != nil {
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
