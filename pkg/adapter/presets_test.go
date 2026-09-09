package adapter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/model"
)

func TestOpenAIAdapter(t *testing.T) {
	adapter := NewOpenAIAdapter()
	if adapter.Type() != "openai" {
		t.Fatalf("expected type 'openai', got %s", adapter.Type())
	}

	agent := &config.AgentConfig{
		ID:       "gpt-agent",
		Type:     "openai",
		Endpoint: "https://api.openai.com/v1/chat/completions",
		Auth: config.AuthConfig{
			Type:  "bearer",
			Token: "sk-test-secret-key",
		},
		Options: map[string]any{
			"model":       "gpt-4o",
			"temperature": 0.7,
		},
	}

	task := &model.TaskRequest{
		ID:     "t-1",
		Input:  "What is A2A?",
		Stream: true,
	}

	// 1. Test TranslateRequest
	req, err := adapter.TranslateRequest(context.Background(), agent, task)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if req.Header.Get("Authorization") != "Bearer sk-test-secret-key" {
		t.Errorf("expected bearer token header, got %s", req.Header.Get("Authorization"))
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("expected application/json Content-Type, got %s", req.Header.Get("Content-Type"))
	}

	body, _ := io.ReadAll(req.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, `"model":"gpt-4o"`) {
		t.Errorf("model missing in body: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"role":"user"`) || !strings.Contains(bodyStr, `"content":"What is A2A?"`) {
		t.Errorf("user message missing in body: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"stream":true`) {
		t.Errorf("stream:true missing in body: %s", bodyStr)
	}

	// 2. Test TranslateResponse success
	fakeResp := `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"A2A is Agent to Agent"}}]}`
	httpResp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(fakeResp)),
	}

	taskResp, err := adapter.TranslateResponse(context.Background(), agent, httpResp)
	if err != nil {
		t.Fatalf("response failed: %v", err)
	}

	if taskResp.Output != "A2A is Agent to Agent" {
		t.Errorf("expected output, got %v", taskResp.Output)
	}
	if taskResp.Status != model.StatusCompleted {
		t.Errorf("expected status COMPLETED, got %v", taskResp.Status)
	}

	// 3. Test TranslateResponse error status code >= 400
	errResp := `{"error":{"message":"Invalid API key"}}`
	httpErrResp := &http.Response{
		StatusCode: 401,
		Body:       io.NopCloser(bytes.NewBufferString(errResp)),
	}
	taskErrResp, err := adapter.TranslateResponse(context.Background(), agent, httpErrResp)
	if err != nil {
		t.Fatalf("translate response error handling failed: %v", err)
	}
	if taskErrResp.Status != model.StatusFailed {
		t.Errorf("expected status FAILED, got %v", taskErrResp.Status)
	}
	if taskErrResp.Error == nil || !strings.Contains(*taskErrResp.Error, "Invalid API key") {
		t.Errorf("expected Invalid API key error message, got %v", taskErrResp.Error)
	}

	// 4. Test TranslateStream success
	streamData := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"choices":[{"delta":{"content":" world"}}]}`,
		`data: [DONE]`,
		"",
	}, "\n")
	streamResp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(streamData)),
	}

	emitter := &mockEmitter{}
	if err := adapter.TranslateStream(context.Background(), agent, streamResp, emitter); err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	events := emitter.Events()
	if len(events) != 4 { // started, delta("Hello"), delta(" world"), completed
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != model.EventTaskStarted {
		t.Errorf("expected event 0 to be task_started, got %s", events[0].Type)
	}
	if events[1].Type != model.EventTokenDelta || events[1].Data.(map[string]any)["delta"] != "Hello" {
		t.Errorf("expected event 1 token_delta 'Hello', got %+v", events[1])
	}
	if events[2].Type != model.EventTokenDelta || events[2].Data.(map[string]any)["delta"] != " world" {
		t.Errorf("expected event 2 token_delta ' world', got %+v", events[2])
	}
	if events[3].Type != model.EventTaskCompleted {
		t.Errorf("expected event 3 task_completed, got %s", events[3].Type)
	}

	// 5. Test TranslateStream error >= 400
	streamErrResp := &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"internal error"}}`)),
	}
	errEmitter := &mockEmitter{}
	streamErr := adapter.TranslateStream(context.Background(), agent, streamErrResp, errEmitter)
	if streamErr == nil {
		t.Fatal("expected error on status 500, got nil")
	}
	errEvents := errEmitter.Events()
	if len(errEvents) != 1 || errEvents[0].Type != model.EventTaskError {
		t.Errorf("expected EventTaskError, got %+v", errEvents)
	}
}

func TestLangGraphAdapter(t *testing.T) {
	adapter := NewLangGraphAdapter()
	if adapter.Type() != "langgraph" {
		t.Fatalf("expected type 'langgraph', got %s", adapter.Type())
	}

	agent := &config.AgentConfig{
		ID:       "lg-agent",
		Type:     "langgraph",
		Endpoint: "http://localhost:8000/runs/wait",
		Auth: config.AuthConfig{
			Type:  "bearer",
			Token: "lg-token",
		},
	}

	// 1. Test TranslateRequest with task.ID as thread_id
	task1 := &model.TaskRequest{
		ID:    "task-lg-1",
		Input: map[string]any{"messages": []string{"hi"}},
	}
	req1, err := adapter.TranslateRequest(context.Background(), agent, task1)
	if err != nil {
		t.Fatalf("translate request failed: %v", err)
	}
	if req1.Header.Get("Authorization") != "Bearer lg-token" {
		t.Errorf("expected bearer token, got %s", req1.Header.Get("Authorization"))
	}
	body1, _ := io.ReadAll(req1.Body)
	body1Str := string(body1)
	if !strings.Contains(body1Str, `"input"`) {
		t.Errorf("expected input wrapper in body: %s", body1Str)
	}
	if !strings.Contains(body1Str, `"thread_id":"task-lg-1"`) {
		t.Errorf("expected thread_id to be task-lg-1 in body: %s", body1Str)
	}

	// 2. Test TranslateRequest with context["thread_id"] override
	task2 := &model.TaskRequest{
		ID:      "task-lg-2",
		Input:   "hello",
		Context: map[string]any{"thread_id": "thread-custom-123"},
	}
	req2, err := adapter.TranslateRequest(context.Background(), agent, task2)
	if err != nil {
		t.Fatalf("translate request 2 failed: %v", err)
	}
	body2, _ := io.ReadAll(req2.Body)
	body2Str := string(body2)
	if !strings.Contains(body2Str, `"thread_id":"thread-custom-123"`) {
		t.Errorf("expected thread_id thread-custom-123 in body: %s", body2Str)
	}

	// 3. Test TranslateResponse with output field
	fakeRespOutput := `{"output":{"result":"LangGraph done"}}`
	httpResp1 := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(fakeRespOutput)),
	}
	taskResp1, err := adapter.TranslateResponse(context.Background(), agent, httpResp1)
	if err != nil {
		t.Fatalf("translate response failed: %v", err)
	}
	outMap, ok := taskResp1.Output.(map[string]any)
	if !ok || outMap["result"] != "LangGraph done" {
		t.Errorf("unexpected output: %+v", taskResp1.Output)
	}

	// 4. Test TranslateResponse with values field fallback
	fakeRespValues := `{"values":{"state":"finished"}}`
	httpResp2 := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(fakeRespValues)),
	}
	taskResp2, err := adapter.TranslateResponse(context.Background(), agent, httpResp2)
	if err != nil {
		t.Fatalf("translate response 2 failed: %v", err)
	}
	valMap, ok := taskResp2.Output.(map[string]any)
	if !ok || valMap["state"] != "finished" {
		t.Errorf("unexpected output: %+v", taskResp2.Output)
	}

	// 5. Test TranslateResponse error status >= 400
	httpErrResp := &http.Response{
		StatusCode: 404,
		Body:       io.NopCloser(bytes.NewBufferString(`{"detail":"thread not found"}`)),
	}
	taskErrResp, err := adapter.TranslateResponse(context.Background(), agent, httpErrResp)
	if err != nil {
		t.Fatalf("translate error response failed: %v", err)
	}
	if taskErrResp.Status != model.StatusFailed {
		t.Errorf("expected status FAILED, got %v", taskErrResp.Status)
	}

	// 6. Test TranslateStream (events: messages, values, end)
	streamData := strings.Join([]string{
		`event: values`,
		`data: {"step":1,"state":"starting"}`,
		``,
		`event: messages`,
		`data: [{"content":"token 1"}]`,
		``,
		`event: messages`,
		`data: {"content":"token 2"}`,
		``,
		`event: end`,
		`data: {"status":"done"}`,
		``,
	}, "\n")
	streamResp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(streamData)),
	}
	emitter := &mockEmitter{}
	if err := adapter.TranslateStream(context.Background(), agent, streamResp, emitter); err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	events := emitter.Events()
	if len(events) < 4 {
		t.Fatalf("expected at least 4 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != model.EventTaskStarted {
		t.Errorf("expected task_started, got %s", events[0].Type)
	}
	if events[1].Type != model.EventStepProgress {
		t.Errorf("expected step_progress for values event, got %s", events[1].Type)
	}
	if events[2].Type != model.EventTokenDelta {
		t.Errorf("expected token_delta for messages event, got %s", events[2].Type)
	}
	if events[len(events)-1].Type != model.EventTaskCompleted {
		t.Errorf("expected task_completed at end, got %s", events[len(events)-1].Type)
	}

	// 7. Test TranslateStream error >= 400
	streamErrResp := &http.Response{
		StatusCode: 502,
		Body:       io.NopCloser(bytes.NewBufferString(`{"error":"bad gateway"}`)),
	}
	errEmitter := &mockEmitter{}
	if err := adapter.TranslateStream(context.Background(), agent, streamErrResp, errEmitter); err == nil {
		t.Fatal("expected error on status 502, got nil")
	}
	if len(errEmitter.Events()) != 1 || errEmitter.Events()[0].Type != model.EventTaskError {
		t.Errorf("expected EventTaskError on 502, got %+v", errEmitter.Events())
	}
}

func TestCrewAIAdapter(t *testing.T) {
	adapter := NewCrewAIAdapter()
	if adapter.Type() != "crewai" {
		t.Fatalf("expected type 'crewai', got %s", adapter.Type())
	}

	agent := &config.AgentConfig{
		ID:       "crew-1",
		Type:     "crewai",
		Endpoint: "http://localhost:5000/kickoff",
		Auth: config.AuthConfig{
			Type:        "header",
			HeaderName:  "X-Crew-Token",
			HeaderValue: "secret-crew",
		},
	}

	// 1. Test TranslateRequest with string input
	task1 := &model.TaskRequest{
		ID:    "task-crew-1",
		Input: "Research quantum computing",
	}
	req1, err := adapter.TranslateRequest(context.Background(), agent, task1)
	if err != nil {
		t.Fatalf("translate request failed: %v", err)
	}
	if req1.Header.Get("X-Crew-Token") != "secret-crew" {
		t.Errorf("expected header auth, got %s", req1.Header.Get("X-Crew-Token"))
	}
	body1, _ := io.ReadAll(req1.Body)
	body1Str := string(body1)
	if !strings.Contains(body1Str, `"inputs"`) {
		t.Errorf("expected inputs wrapper in body: %s", body1Str)
	}

	// 2. Test TranslateRequest with map input
	task2 := &model.TaskRequest{
		ID:    "task-crew-2",
		Input: map[string]any{"topic": "AI agents"},
	}
	req2, err := adapter.TranslateRequest(context.Background(), agent, task2)
	if err != nil {
		t.Fatalf("translate request 2 failed: %v", err)
	}
	body2, _ := io.ReadAll(req2.Body)
	body2Str := string(body2)
	if !strings.Contains(body2Str, `"topic":"AI agents"`) {
		t.Errorf("expected topic in body: %s", body2Str)
	}

	// 3. Test TranslateResponse with "result"
	fakeRespResult := `{"result":"Crew finished successfully","status":"SUCCESS"}`
	httpResp1 := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(fakeRespResult)),
	}
	taskResp1, err := adapter.TranslateResponse(context.Background(), agent, httpResp1)
	if err != nil {
		t.Fatalf("translate response failed: %v", err)
	}
	if taskResp1.Output != "Crew finished successfully" {
		t.Errorf("expected output, got %v", taskResp1.Output)
	}
	if taskResp1.Status != model.StatusCompleted {
		t.Errorf("expected completed status, got %s", taskResp1.Status)
	}

	// 4. Test TranslateResponse with "raw" output
	fakeRespRaw := `{"raw":"Raw analysis output"}`
	httpResp2 := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(fakeRespRaw)),
	}
	taskResp2, err := adapter.TranslateResponse(context.Background(), agent, httpResp2)
	if err != nil {
		t.Fatalf("translate response 2 failed: %v", err)
	}
	if taskResp2.Output != "Raw analysis output" {
		t.Errorf("expected raw output, got %v", taskResp2.Output)
	}

	// 5. Test TranslateResponse error status >= 400
	httpErrResp := &http.Response{
		StatusCode: 400,
		Body:       io.NopCloser(bytes.NewBufferString(`{"error":"missing required inputs"}`)),
	}
	taskErrResp, err := adapter.TranslateResponse(context.Background(), agent, httpErrResp)
	if err != nil {
		t.Fatalf("translate response err failed: %v", err)
	}
	if taskErrResp.Status != model.StatusFailed {
		t.Errorf("expected FAILED status, got %s", taskErrResp.Status)
	}

	// 6. Test TranslateStream
	streamData := strings.Join([]string{
		`data: {"thought":"Thinking about task..."}`,
		`data: {"delta":"Quantum computing is..."}`,
		`data: [DONE]`,
		"",
	}, "\n")
	streamResp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(streamData)),
	}
	emitter := &mockEmitter{}
	if err := adapter.TranslateStream(context.Background(), agent, streamResp, emitter); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	events := emitter.Events()
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != model.EventTaskStarted {
		t.Errorf("expected task_started, got %s", events[0].Type)
	}
	if events[1].Type != model.EventStepProgress {
		t.Errorf("expected step_progress for thought, got %s", events[1].Type)
	}
	if events[2].Type != model.EventTokenDelta {
		t.Errorf("expected token_delta for delta, got %s", events[2].Type)
	}
	if events[3].Type != model.EventTaskCompleted {
		t.Errorf("expected task_completed, got %s", events[3].Type)
	}

	// 7. Test TranslateStream error >= 400
	streamErrResp := &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(bytes.NewBufferString(`server crashed`)),
	}
	errEmitter := &mockEmitter{}
	if err := adapter.TranslateStream(context.Background(), agent, streamErrResp, errEmitter); err == nil {
		t.Fatal("expected error on 500 status, got nil")
	}
	if len(errEmitter.Events()) != 1 || errEmitter.Events()[0].Type != model.EventTaskError {
		t.Errorf("expected EventTaskError, got %+v", errEmitter.Events())
	}
}

func TestAutoGenAdapter(t *testing.T) {
	adapter := NewAutoGenAdapter()
	if adapter.Type() != "autogen" {
		t.Fatalf("expected type 'autogen', got %s", adapter.Type())
	}

	agent := &config.AgentConfig{
		ID:       "autogen-agent",
		Type:     "autogen",
		Endpoint: "http://localhost:8088/chat",
		Auth: config.AuthConfig{
			Type:  "bearer",
			Token: "ag-token",
		},
		Options: map[string]any{
			"sender":    "default-user",
			"recipient": "assistant-bot",
		},
	}

	// 1. Test TranslateRequest default options
	task1 := &model.TaskRequest{
		ID:    "task-ag-1",
		Input: "Write a poem",
	}
	req1, err := adapter.TranslateRequest(context.Background(), agent, task1)
	if err != nil {
		t.Fatalf("translate request failed: %v", err)
	}
	if req1.Header.Get("Authorization") != "Bearer ag-token" {
		t.Errorf("expected bearer token, got %s", req1.Header.Get("Authorization"))
	}
	body1, _ := io.ReadAll(req1.Body)
	body1Str := string(body1)
	if !strings.Contains(body1Str, `"sender":"default-user"`) {
		t.Errorf("expected sender in body: %s", body1Str)
	}
	if !strings.Contains(body1Str, `"recipient":"assistant-bot"`) {
		t.Errorf("expected recipient in body: %s", body1Str)
	}
	if !strings.Contains(body1Str, `"message":"Write a poem"`) {
		t.Errorf("expected message in body: %s", body1Str)
	}

	// 2. Test TranslateRequest context overrides
	task2 := &model.TaskRequest{
		ID:    "task-ag-2",
		Input: "Hello",
		Context: map[string]any{
			"sender":    "custom-sender",
			"recipient": "custom-recipient",
		},
	}
	req2, err := adapter.TranslateRequest(context.Background(), agent, task2)
	if err != nil {
		t.Fatalf("translate request 2 failed: %v", err)
	}
	body2, _ := io.ReadAll(req2.Body)
	body2Str := string(body2)
	if !strings.Contains(body2Str, `"sender":"custom-sender"`) {
		t.Errorf("expected overridden sender, got %s", body2Str)
	}
	if !strings.Contains(body2Str, `"recipient":"custom-recipient"`) {
		t.Errorf("expected overridden recipient, got %s", body2Str)
	}

	// 3. Test TranslateResponse reply field
	fakeRespReply := `{"reply":"Here is a poem"}`
	httpResp1 := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(fakeRespReply)),
	}
	taskResp1, err := adapter.TranslateResponse(context.Background(), agent, httpResp1)
	if err != nil {
		t.Fatalf("translate response failed: %v", err)
	}
	if taskResp1.Output != "Here is a poem" {
		t.Errorf("expected reply output, got %v", taskResp1.Output)
	}

	// 4. Test TranslateResponse chat_history fallback
	fakeRespHist := `{"chat_history":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Final response"}]}`
	httpResp2 := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(fakeRespHist)),
	}
	taskResp2, err := adapter.TranslateResponse(context.Background(), agent, httpResp2)
	if err != nil {
		t.Fatalf("translate response 2 failed: %v", err)
	}
	if taskResp2.Output != "Final response" {
		t.Errorf("expected final response from chat_history, got %v", taskResp2.Output)
	}

	// 5. Test TranslateResponse error status >= 400
	httpErrResp := &http.Response{
		StatusCode: 400,
		Body:       io.NopCloser(bytes.NewBufferString(`{"error":"bad request"}`)),
	}
	taskErrResp, err := adapter.TranslateResponse(context.Background(), agent, httpErrResp)
	if err != nil {
		t.Fatalf("translate err response failed: %v", err)
	}
	if taskErrResp.Status != model.StatusFailed {
		t.Errorf("expected FAILED status, got %s", taskErrResp.Status)
	}

	// 6. Test TranslateStream
	streamData := strings.Join([]string{
		`data: {"step":"agent_reflection"}`,
		`data: {"content":"Roses are red"}`,
		`data: [DONE]`,
		"",
	}, "\n")
	streamResp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(streamData)),
	}
	emitter := &mockEmitter{}
	if err := adapter.TranslateStream(context.Background(), agent, streamResp, emitter); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	events := emitter.Events()
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != model.EventTaskStarted {
		t.Errorf("expected task_started, got %s", events[0].Type)
	}
	if events[1].Type != model.EventStepProgress {
		t.Errorf("expected step_progress, got %s", events[1].Type)
	}
	if events[2].Type != model.EventTokenDelta {
		t.Errorf("expected token_delta, got %s", events[2].Type)
	}
	if events[3].Type != model.EventTaskCompleted {
		t.Errorf("expected task_completed, got %s", events[3].Type)
	}

	// 7. Test TranslateStream error >= 400
	streamErrResp := &http.Response{
		StatusCode: 503,
		Body:       io.NopCloser(bytes.NewBufferString(`service unavailable`)),
	}
	errEmitter := &mockEmitter{}
	if err := adapter.TranslateStream(context.Background(), agent, streamErrResp, errEmitter); err == nil {
		t.Fatal("expected error on 503 status, got nil")
	}
	if len(errEmitter.Events()) != 1 || errEmitter.Events()[0].Type != model.EventTaskError {
		t.Errorf("expected EventTaskError, got %+v", errEmitter.Events())
	}
}

func TestLargeLinesAndBufferInStream(t *testing.T) {
	// Verify 10MB scanner buffer works on all 4 adapters with lines > 64KB (e.g. 128KB line)
	largeData := strings.Repeat("A", 128*1024)
	largeSSE := fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":\"%s\"}}]}\ndata: [DONE]\n", largeData)

	adapter := NewOpenAIAdapter()
	agent := &config.AgentConfig{ID: "large-agent", Type: "openai", Endpoint: "http://localhost"}
	streamResp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(largeSSE)),
	}
	emitter := &mockEmitter{}
	if err := adapter.TranslateStream(context.Background(), agent, streamResp, emitter); err != nil {
		t.Fatalf("failed handling large line: %v", err)
	}
	events := emitter.Events()
	if len(events) != 3 { // started, delta, completed
		t.Fatalf("expected 3 events, got %d", len(events))
	}
}

func TestRegistryWithAllPresets(t *testing.T) {
	reg := NewRegistry()
	reg.Register(NewCustomAdapter())
	reg.Register(NewLangGraphAdapter())
	reg.Register(NewCrewAIAdapter())
	reg.Register(NewAutoGenAdapter())
	reg.Register(NewOpenAIAdapter())

	expectedTypes := []string{"custom", "langgraph", "crewai", "autogen", "openai"}
	for _, expected := range expectedTypes {
		a, err := reg.Get(expected)
		if err != nil {
			t.Errorf("failed to get adapter for %s: %v", expected, err)
		}
		if a.Type() != expected {
			t.Errorf("expected adapter type %s, got %s", expected, a.Type())
		}
	}
}

func TestPresetsNilChecks(t *testing.T) {
	adapters := []Adapter{
		NewLangGraphAdapter(),
		NewCrewAIAdapter(),
		NewAutoGenAdapter(),
		NewOpenAIAdapter(),
	}

	for _, a := range adapters {
		t.Run(a.Type()+"_nil_request", func(t *testing.T) {
			if _, err := a.TranslateRequest(context.Background(), nil, nil); err == nil {
				t.Errorf("%s: expected error on nil agent/task", a.Type())
			}
		})

		t.Run(a.Type()+"_nil_response", func(t *testing.T) {
			agent := &config.AgentConfig{ID: "test"}
			if _, err := a.TranslateResponse(context.Background(), agent, nil); err == nil {
				t.Errorf("%s: expected error on nil response", a.Type())
			}
			if _, err := a.TranslateResponse(context.Background(), agent, &http.Response{}); err == nil {
				t.Errorf("%s: expected error on response with nil body", a.Type())
			}
		})

		t.Run(a.Type()+"_nil_stream", func(t *testing.T) {
			agent := &config.AgentConfig{ID: "test"}
			emitter := &mockEmitter{}
			if err := a.TranslateStream(context.Background(), agent, nil, emitter); err == nil {
				t.Errorf("%s: expected error on nil response", a.Type())
			}
			if err := a.TranslateStream(context.Background(), agent, &http.Response{}, emitter); err == nil {
				t.Errorf("%s: expected error on response with nil body", a.Type())
			}
		})
	}
}

func TestPresetsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	adapters := []Adapter{
		NewLangGraphAdapter(),
		NewCrewAIAdapter(),
		NewAutoGenAdapter(),
		NewOpenAIAdapter(),
	}

	for _, a := range adapters {
		t.Run(a.Type()+"_stream_canceled", func(t *testing.T) {
			agent := &config.AgentConfig{ID: "test"}
			streamData := "data: line 1\ndata: line 2\n"
			httpResp := &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(streamData)),
			}
			emitter := &mockEmitter{}
			err := a.TranslateStream(ctx, agent, httpResp, emitter)
			if err == nil || !strings.Contains(err.Error(), "context canceled") {
				t.Errorf("%s: expected context canceled error, got %v", a.Type(), err)
			}
		})
	}
}

func TestOpenAIOptionsAndVariedInputs(t *testing.T) {
	adapter := NewOpenAIAdapter()
	agent := &config.AgentConfig{
		ID:       "opt-agent",
		Type:     "openai",
		Endpoint: "http://localhost/v1/chat/completions",
		Options: map[string]any{
			"model":         "gpt-3.5-turbo",
			"system_prompt": "You are a helpful coder.",
			"max_tokens":    150,
		},
	}

	// 1. Task with []map[string]any messages
	task1 := &model.TaskRequest{
		ID: "t-1",
		Input: []map[string]any{
			{"role": "user", "content": "hello"},
		},
	}
	req1, err := adapter.TranslateRequest(context.Background(), agent, task1)
	if err != nil {
		t.Fatalf("translate failed: %v", err)
	}
	body1, _ := io.ReadAll(req1.Body)
	if !strings.Contains(string(body1), "You are a helpful coder.") {
		t.Errorf("expected system prompt in body: %s", string(body1))
	}
	if !strings.Contains(string(body1), `"max_tokens":150`) {
		t.Errorf("expected max_tokens in body: %s", string(body1))
	}

	// 2. Task with map containing "messages"
	task2 := &model.TaskRequest{
		ID: "t-2",
		Input: map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "ping"},
			},
		},
		Context: map[string]any{
			"system": "Context system override",
		},
	}
	req2, err := adapter.TranslateRequest(context.Background(), agent, task2)
	if err != nil {
		t.Fatalf("translate failed: %v", err)
	}
	body2, _ := io.ReadAll(req2.Body)
	if !strings.Contains(string(body2), "Context system override") {
		t.Errorf("expected context system override in body: %s", string(body2))
	}

	// 3. Fallback text output response
	fakeRespText := `{"choices":[{"text":"legacy completion text"}]}`
	httpResp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(fakeRespText)),
	}
	taskResp, err := adapter.TranslateResponse(context.Background(), agent, httpResp)
	if err != nil {
		t.Fatalf("translate response failed: %v", err)
	}
	if taskResp.Output != "legacy completion text" {
		t.Errorf("expected legacy completion text, got %v", taskResp.Output)
	}
}

