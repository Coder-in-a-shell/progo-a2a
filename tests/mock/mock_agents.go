package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/tidwall/gjson"
)

// MockServerConfig configures recording and retention behavior for mock servers.
type MockServerConfig struct {
	DisableRecording bool
	MaxRecorded      int // maximum number of requests/bodies to retain; <=0 defaults to 10
}

func parseMockConfig(cfgs []MockServerConfig) (bool, int) {
	if len(cfgs) == 0 {
		return false, 10
	}
	max := cfgs[0].MaxRecorded
	if max <= 0 {
		max = 10
	}
	return cfgs[0].DisableRecording, max
}

// MockLangGraphServer mocks LangGraph SSE streaming and stateful runs.
type MockLangGraphServer struct {
	Server           *httptest.Server
	URL              string
	mu               sync.Mutex
	threadStates     map[string][]string
	requests         []*http.Request
	recordedBodies   [][]byte
	disableRecording bool
	maxRecorded      int
	lastHeader       http.Header
	requestCount     int
}

// NewMockLangGraphServer creates and starts a new MockLangGraphServer.
func NewMockLangGraphServer(cfgs ...MockServerConfig) *MockLangGraphServer {
	disableRec, maxRec := parseMockConfig(cfgs)
	m := &MockLangGraphServer{
		threadStates:     make(map[string][]string),
		disableRecording: disableRec,
		maxRecorded:      maxRec,
	}

	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		threadID := gjson.GetBytes(body, "config.configurable.thread_id").String()
		if threadID == "" {
			threadID = "default-thread"
		}

		m.mu.Lock()
		m.requestCount++
		if !m.disableRecording {
			m.lastHeader = r.Header.Clone()
			if len(m.requests) >= m.maxRecorded {
				m.requests = m.requests[1:]
				m.recordedBodies = m.recordedBodies[1:]
			}
			m.requests = append(m.requests, r)
			m.recordedBodies = append(m.recordedBodies, body)

			inputText := gjson.GetBytes(body, "input").String()
			if len(m.threadStates[threadID]) >= m.maxRecorded {
				m.threadStates[threadID] = m.threadStates[threadID][1:]
			}
			m.threadStates[threadID] = append(m.threadStates[threadID], inputText)
		}
		historyCount := len(m.threadStates[threadID])
		m.mu.Unlock()

		isStream := r.Header.Get("Accept") == "text/event-stream" ||
			strings.Contains(r.URL.Path, "stream") ||
			gjson.GetBytes(body, "stream_mode").Exists()

		if isStream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
				return
			}

			// event: values (state progress)
			_, _ = fmt.Fprintf(w, "event: values\ndata: {\"thread_id\":\"%s\",\"step\":1}\n\n", threadID)
			flusher.Flush()

			// event: messages (token delta)
			_, _ = fmt.Fprintf(w, "event: messages\ndata: [{\"content\":\"LangGraph message 1 for %s\"}]\n\n", threadID)
			flusher.Flush()

			// event: values (second step)
			_, _ = fmt.Fprintf(w, "event: values\ndata: {\"thread_id\":\"%s\",\"step\":2}\n\n", threadID)
			flusher.Flush()

			// event: messages (second token delta)
			_, _ = fmt.Fprintf(w, "event: messages\ndata: [{\"content\":\" LangGraph message 2 for %s\"}]\n\n", threadID)
			flusher.Flush()

			// end sentinel
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output":    fmt.Sprintf("LangGraph output for thread %s", threadID),
			"thread_id": threadID,
			"values": map[string]any{
				"history_count": historyCount,
			},
		})
	}))

	m.URL = m.Server.URL
	return m
}

func (m *MockLangGraphServer) Close() {
	m.Server.Close()
}

func (m *MockLangGraphServer) GetThreadHistory(threadID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.threadStates[threadID]...)
}

func (m *MockLangGraphServer) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requestCount
}

func (m *MockLangGraphServer) LastHeader(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastHeader != nil {
		return m.lastHeader.Get(name)
	}
	if len(m.requests) > 0 {
		return m.requests[len(m.requests)-1].Header.Get(name)
	}
	return ""
}

func (m *MockLangGraphServer) SetDisableRecording(disabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disableRecording = disabled
}

// MockCrewAIServer mocks CrewAI /kickoff JSON and streaming responses.
type MockCrewAIServer struct {
	Server           *httptest.Server
	URL              string
	mu               sync.Mutex
	requests         []*http.Request
	recordedBodies   [][]byte
	disableRecording bool
	maxRecorded      int
	lastHeader       http.Header
	requestCount     int
}

// NewMockCrewAIServer creates and starts a new MockCrewAIServer.
func NewMockCrewAIServer(cfgs ...MockServerConfig) *MockCrewAIServer {
	disableRec, maxRec := parseMockConfig(cfgs)
	m := &MockCrewAIServer{
		disableRecording: disableRec,
		maxRecorded:      maxRec,
	}

	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		m.mu.Lock()
		m.requestCount++
		if !m.disableRecording {
			m.lastHeader = r.Header.Clone()
			if len(m.requests) >= m.maxRecorded {
				m.requests = m.requests[1:]
				m.recordedBodies = m.recordedBodies[1:]
			}
			m.requests = append(m.requests, r)
			m.recordedBodies = append(m.recordedBodies, body)
		}
		m.mu.Unlock()

		isStream := r.Header.Get("Accept") == "text/event-stream" || strings.Contains(r.URL.Path, "stream")

		if isStream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
				return
			}

			// step thought
			_, _ = fmt.Fprint(w, "data: {\"thought\": \"Crew researching topic\"}\n\n")
			flusher.Flush()

			// step action
			_, _ = fmt.Fprint(w, "data: {\"action\": \"Delegating to senior researcher\"}\n\n")
			flusher.Flush()

			// token delta
			_, _ = fmt.Fprint(w, "data: {\"delta\": \"CrewAI kickoff streaming delta\"}\n\n")
			flusher.Flush()

			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": "CrewAI task successfully finished",
			"status": "SUCCESS",
			"tasks_output": []map[string]any{
				{"name": "task-1", "output": "CrewAI task successfully finished"},
			},
		})
	}))

	m.URL = m.Server.URL
	return m
}

func (m *MockCrewAIServer) Close() {
	m.Server.Close()
}

func (m *MockCrewAIServer) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requestCount
}

func (m *MockCrewAIServer) LastHeader(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastHeader != nil {
		return m.lastHeader.Get(name)
	}
	if len(m.requests) > 0 {
		return m.requests[len(m.requests)-1].Header.Get(name)
	}
	return ""
}

func (m *MockCrewAIServer) SetDisableRecording(disabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disableRecording = disabled
}

// MockAutoGenServer mocks AutoGen /chat responses and streaming.
type MockAutoGenServer struct {
	Server           *httptest.Server
	URL              string
	mu               sync.Mutex
	requests         []*http.Request
	recordedBodies   [][]byte
	disableRecording bool
	maxRecorded      int
	lastHeader       http.Header
	requestCount     int
}

// NewMockAutoGenServer creates and starts a new MockAutoGenServer.
func NewMockAutoGenServer(cfgs ...MockServerConfig) *MockAutoGenServer {
	disableRec, maxRec := parseMockConfig(cfgs)
	m := &MockAutoGenServer{
		disableRecording: disableRec,
		maxRecorded:      maxRec,
	}

	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		m.mu.Lock()
		m.requestCount++
		if !m.disableRecording {
			m.lastHeader = r.Header.Clone()
			if len(m.requests) >= m.maxRecorded {
				m.requests = m.requests[1:]
				m.recordedBodies = m.recordedBodies[1:]
			}
			m.requests = append(m.requests, r)
			m.recordedBodies = append(m.recordedBodies, body)
		}
		m.mu.Unlock()

		isStream := r.Header.Get("Accept") == "text/event-stream" || strings.Contains(r.URL.Path, "stream")

		if isStream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
				return
			}

			// progress step
			_, _ = fmt.Fprint(w, "data: {\"step\": \"AutoGen agent formulating reply\"}\n\n")
			flusher.Flush()

			// content piece
			_, _ = fmt.Fprint(w, "data: {\"content\": \"AutoGen streaming token part\"}\n\n")
			flusher.Flush()

			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reply": "AutoGen conversation reply message",
			"chat_history": []map[string]string{
				{"role": "assistant", "content": "AutoGen conversation reply message"},
			},
		})
	}))

	m.URL = m.Server.URL
	return m
}

func (m *MockAutoGenServer) Close() {
	m.Server.Close()
}

func (m *MockAutoGenServer) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requestCount
}

func (m *MockAutoGenServer) LastHeader(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastHeader != nil {
		return m.lastHeader.Get(name)
	}
	if len(m.requests) > 0 {
		return m.requests[len(m.requests)-1].Header.Get(name)
	}
	return ""
}

func (m *MockAutoGenServer) SetDisableRecording(disabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disableRecording = disabled
}

// MockOpenAIServer mocks OpenAI /v1/chat/completions endpoint.
type MockOpenAIServer struct {
	Server           *httptest.Server
	URL              string
	mu               sync.Mutex
	requests         []*http.Request
	recordedBodies   [][]byte
	disableRecording bool
	maxRecorded      int
	lastHeader       http.Header
	requestCount     int
}

// NewMockOpenAIServer creates and starts a new MockOpenAIServer.
func NewMockOpenAIServer(cfgs ...MockServerConfig) *MockOpenAIServer {
	disableRec, maxRec := parseMockConfig(cfgs)
	m := &MockOpenAIServer{
		disableRecording: disableRec,
		maxRecorded:      maxRec,
	}

	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		m.mu.Lock()
		m.requestCount++
		if !m.disableRecording {
			m.lastHeader = r.Header.Clone()
			if len(m.requests) >= m.maxRecorded {
				m.requests = m.requests[1:]
				m.recordedBodies = m.recordedBodies[1:]
			}
			m.requests = append(m.requests, r)
			m.recordedBodies = append(m.recordedBodies, body)
		}
		m.mu.Unlock()

		isStream := r.Header.Get("Accept") == "text/event-stream" ||
			gjson.GetBytes(body, "stream").Bool()

		if isStream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
				return
			}

			chunks := []string{"Hello ", "from ", "OpenAI ", "mock!"}
			for _, chunk := range chunks {
				payload := map[string]any{
					"id":     "chatcmpl-mock",
					"object": "chat.completion.chunk",
					"choices": []map[string]any{
						{
							"index": 0,
							"delta": map[string]any{"content": chunk},
						},
					},
				}
				jsonBytes, _ := json.Marshal(payload)
				_, _ = fmt.Fprintf(w, "data: %s\n\n", string(jsonBytes))
				flusher.Flush()
			}

			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "chatcmpl-mock-123",
			"object": "chat.completion",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "OpenAI mock completed successfully",
					},
					"finish_reason": "stop",
				},
			},
		})
	}))

	m.URL = m.Server.URL
	return m
}

func (m *MockOpenAIServer) Close() {
	m.Server.Close()
}

func (m *MockOpenAIServer) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requestCount
}

func (m *MockOpenAIServer) LastHeader(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastHeader != nil {
		return m.lastHeader.Get(name)
	}
	if len(m.requests) > 0 {
		return m.requests[len(m.requests)-1].Header.Get(name)
	}
	return ""
}

func (m *MockOpenAIServer) SetDisableRecording(disabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disableRecording = disabled
}

// MockCustomServer mocks arbitrary HTTP webhooks with customizable behavior.
type MockCustomServer struct {
	Server           *httptest.Server
	URL              string
	mu               sync.Mutex
	requests         []*http.Request
	recordedBodies   [][]byte
	disableRecording bool
	maxRecorded      int
	lastHeader       http.Header
	lastBody         []byte
	requestCount     int
}

// NewMockCustomServer creates and starts a new MockCustomServer with default configuration.
func NewMockCustomServer(customHandler ...http.HandlerFunc) *MockCustomServer {
	return NewMockCustomServerWithConfig(MockServerConfig{}, customHandler...)
}

// NewMockCustomServerWithConfig creates and starts a new MockCustomServer with specific config.
func NewMockCustomServerWithConfig(cfg MockServerConfig, customHandler ...http.HandlerFunc) *MockCustomServer {
	maxRec := cfg.MaxRecorded
	if maxRec <= 0 {
		maxRec = 10
	}
	m := &MockCustomServer{
		disableRecording: cfg.DisableRecording,
		maxRecorded:      maxRec,
	}

	var handler http.HandlerFunc
	if len(customHandler) > 0 && customHandler[0] != nil {
		handler = customHandler[0]
	} else {
		handler = func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Accept") == "text/event-stream" {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, _ := w.(http.Flusher)

				_, _ = fmt.Fprint(w, "data: {\"text\": \"Custom delta 1\"}\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				_, _ = fmt.Fprint(w, "data: {\"text\": \"Custom delta 2\"}\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "completed",
				"output": "Custom webhook mock output",
				"artifacts": []map[string]any{
					{
						"name":      "analysis.txt",
						"mime_type": "text/plain",
						"data":      "sample data payload",
					},
				},
			})
		}
	}

	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		m.mu.Lock()
		m.requestCount++
		if !m.disableRecording {
			m.lastHeader = r.Header.Clone()
			m.lastBody = body
			if len(m.requests) >= m.maxRecorded {
				m.requests = m.requests[1:]
				m.recordedBodies = m.recordedBodies[1:]
			}
			m.requests = append(m.requests, r)
			m.recordedBodies = append(m.recordedBodies, body)
		}
		m.mu.Unlock()

		handler(w, r)
	}))

	m.URL = m.Server.URL
	return m
}

func (m *MockCustomServer) Close() {
	m.Server.Close()
}

func (m *MockCustomServer) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requestCount
}

func (m *MockCustomServer) LastHeader(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastHeader != nil {
		return m.lastHeader.Get(name)
	}
	if len(m.requests) > 0 {
		return m.requests[len(m.requests)-1].Header.Get(name)
	}
	return ""
}

func (m *MockCustomServer) LastBody() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.lastBody) > 0 {
		return m.lastBody
	}
	if len(m.recordedBodies) > 0 {
		return m.recordedBodies[len(m.recordedBodies)-1]
	}
	return nil
}

func (m *MockCustomServer) SetDisableRecording(disabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disableRecording = disabled
}

// TransientFailureServer fails N times with 500 then calls successHandler or returns 200.
type TransientFailureServer struct {
	Server                *httptest.Server
	URL                   string
	failuresBeforeSuccess int
	attempts              atomic.Int64
	successHandler        http.HandlerFunc
}

// NewTransientFailureServer creates a server that fails failuresBeforeSuccess times with 500 then succeeds.
func NewTransientFailureServer(failuresBeforeSuccess int, successHandler ...http.HandlerFunc) *TransientFailureServer {
	tfs := &TransientFailureServer{
		failuresBeforeSuccess: failuresBeforeSuccess,
	}
	if len(successHandler) > 0 && successHandler[0] != nil {
		tfs.successHandler = successHandler[0]
	}

	tfs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := tfs.attempts.Add(1)
		if int(attempt) <= tfs.failuresBeforeSuccess {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": fmt.Sprintf("simulated failure attempt %d", attempt),
			})
			return
		}

		if tfs.successHandler != nil {
			tfs.successHandler(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "completed",
			"output": fmt.Sprintf("recovered on attempt %d", attempt),
		})
	}))

	tfs.URL = tfs.Server.URL
	return tfs
}

func (tfs *TransientFailureServer) Close() {
	tfs.Server.Close()
}

func (tfs *TransientFailureServer) Attempts() int64 {
	return tfs.attempts.Load()
}

func (tfs *TransientFailureServer) Reset() {
	tfs.attempts.Store(0)
}
