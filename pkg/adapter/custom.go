package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/stream"
	"github.com/tidwall/gjson"
)

var (
	jsonPathIndexRegex = regexp.MustCompile(`\[(\d+)\]`)
	jsonPathPropRegex  = regexp.MustCompile(`\[['"]([^'"]+)['"]\]`)
)

func normalizeJSONPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.TrimPrefix(p, "$.")
	p = strings.TrimPrefix(p, "$")
	p = jsonPathPropRegex.ReplaceAllString(p, ".$1")
	p = jsonPathIndexRegex.ReplaceAllString(p, ".$1")
	for strings.Contains(p, "..") {
		p = strings.ReplaceAll(p, "..", ".")
	}
	p = strings.TrimPrefix(p, ".")
	return p
}

type CustomAdapter struct {
	tmplCache sync.Map
}

func NewCustomAdapter() *CustomAdapter {
	return &CustomAdapter{}
}

func (a *CustomAdapter) Type() string {
	return "custom"
}

func (a *CustomAdapter) getTemplate(bodyTmpl string) (*template.Template, error) {
	if val, ok := a.tmplCache.Load(bodyTmpl); ok {
		return val.(*template.Template), nil
	}

	tmpl, err := template.New("request_body").Funcs(templateFuncs()).Parse(bodyTmpl)
	if err != nil {
		return nil, err
	}

	actual, _ := a.tmplCache.LoadOrStore(bodyTmpl, tmpl)
	return actual.(*template.Template), nil
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"toJson": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
		"default": func(def any, val any) any {
			if val == nil {
				return def
			}
			switch v := val.(type) {
			case string:
				if v == "" {
					return def
				}
			}
			rv := reflect.ValueOf(val)
			switch rv.Kind() {
			case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
				if rv.IsNil() {
					return def
				}
			}
			return val
		},
		"env": func(key string) string {
			return os.Getenv(key)
		},
	}
}

func (a *CustomAdapter) TranslateRequest(ctx context.Context, agent *config.AgentConfig, task *model.TaskRequest) (*http.Request, error) {
	if agent == nil || task == nil {
		return nil, fmt.Errorf("agent and task must not be nil")
	}
	if agent.Mapping == nil {
		return nil, fmt.Errorf("custom agent %s missing mapping config", agent.ID)
	}

	reqTmpl := agent.Mapping.Request
	method := reqTmpl.Method
	if method == "" {
		method = "POST"
	}

	var bodyReader io.Reader
	if strings.TrimSpace(reqTmpl.BodyTemplate) != "" {
		tmpl, err := a.getTemplate(reqTmpl.BodyTemplate)
		if err != nil {
			return nil, fmt.Errorf("failed to parse body template: %w", err)
		}

		templateCtx := map[string]any{
			"Task":  task,
			"Agent": agent,
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, templateCtx); err != nil {
			return nil, fmt.Errorf("failed to execute body template: %w", err)
		}
		if buf.Len() > 0 {
			bodyReader = &buf
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, agent.Endpoint, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	for k, v := range reqTmpl.Headers {
		req.Header.Set(k, v)
	}

	if task.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}

	applyOutboundAuth(req, agent.Auth)
	return req, nil
}

func (a *CustomAdapter) TranslateResponse(ctx context.Context, agent *config.AgentConfig, resp *http.Response) (*model.TaskResponse, error) {
	if agent == nil {
		return nil, fmt.Errorf("agent must not be nil")
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("response or response body is nil")
	}
	defer resp.Body.Close()

	if agent.Mapping == nil {
		return nil, fmt.Errorf("custom agent %s missing mapping config", agent.ID)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	respMap := agent.Mapping.Response
	bodyStr := string(body)

	taskResp := &model.TaskResponse{
		AgentID:   agent.ID,
		Timestamp: time.Now().UTC(),
		Status:    model.StatusCompleted,
	}

	if resp.StatusCode >= 400 {
		taskResp.Status = model.StatusFailed
		errStr := fmt.Sprintf("upstream returned status %d: %s", resp.StatusCode, bodyStr)
		if respMap.ErrorPath != "" {
			extracted := gjson.Get(bodyStr, normalizeJSONPath(respMap.ErrorPath))
			if extracted.Exists() {
				errStr = extracted.String()
			}
		}
		taskResp.Error = &errStr
		return taskResp, nil
	}

	if respMap.OutputPath != "" {
		val := gjson.Get(bodyStr, normalizeJSONPath(respMap.OutputPath))
		if val.Exists() {
			taskResp.Output = val.Value()
		}
	} else {
		taskResp.Output = bodyStr
	}

	if respMap.StatusPath != "" {
		val := gjson.Get(bodyStr, normalizeJSONPath(respMap.StatusPath))
		if val.Exists() {
			s := strings.ToLower(val.String())
			switch s {
			case "error", "failed":
				taskResp.Status = model.StatusFailed
			case "canceled", "cancelled":
				taskResp.Status = model.StatusCanceled
			case "in_progress", "running":
				taskResp.Status = model.StatusInProgress
			case "pending":
				taskResp.Status = model.StatusPending
			case "success", "completed", "done":
				taskResp.Status = model.StatusCompleted
			}
		}
	}

	if taskResp.Status == model.StatusFailed && respMap.ErrorPath != "" && taskResp.Error == nil {
		extracted := gjson.Get(bodyStr, normalizeJSONPath(respMap.ErrorPath))
		if extracted.Exists() {
			errStr := extracted.String()
			taskResp.Error = &errStr
		}
	}

	if respMap.ArtifactsPath != "" {
		val := gjson.Get(bodyStr, normalizeJSONPath(respMap.ArtifactsPath))
		if val.Exists() && val.IsArray() {
			var artifacts []model.Artifact
			for _, item := range val.Array() {
				if item.IsObject() {
					art := model.Artifact{
						Name:     item.Get("name").String(),
						MimeType: item.Get("mime_type").String(),
						URI:      item.Get("uri").String(),
						Data:     item.Get("data").Value(),
					}
					artifacts = append(artifacts, art)
				}
			}
			taskResp.Artifacts = artifacts
		}
	}

	return taskResp, nil
}

func (a *CustomAdapter) TranslateStream(ctx context.Context, agent *config.AgentConfig, resp *http.Response, emitter stream.Emitter) error {
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

	dataPath := ""
	doneSentinel := ""
	if agent.Mapping != nil {
		dataPath = agent.Mapping.Stream.DataPath
		doneSentinel = agent.Mapping.Stream.DoneSentinel
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
			if doneSentinel != "" && data == doneSentinel {
				break
			}

			var deltaText any = data
			if dataPath != "" {
				extracted := gjson.Get(data, normalizeJSONPath(dataPath))
				if extracted.Exists() {
					deltaText = extracted.Value()
				}
			}
			if err := emitter.Emit(model.EventTokenDelta, map[string]any{"delta": deltaText}); err != nil {
				return err
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return emitter.Emit(model.EventTaskCompleted, map[string]any{"status": model.StatusCompleted})
}

func applyOutboundAuth(req *http.Request, auth config.AuthConfig) {
	switch auth.Type {
	case "bearer":
		if auth.Token != "" {
			req.Header.Set("Authorization", "Bearer "+auth.Token)
		}
	case "header":
		if auth.HeaderName != "" && auth.HeaderValue != "" {
			req.Header.Set(auth.HeaderName, auth.HeaderValue)
		}
	}
}
