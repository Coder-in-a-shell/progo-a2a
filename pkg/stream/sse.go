package stream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"a2a-proxy/pkg/model"
)

var _ Emitter = (*SSEResponseWriter)(nil)

type SSEResponseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
}

func NewSSEResponseWriter(w http.ResponseWriter) (*SSEResponseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming unsupported: response writer does not implement http.Flusher")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	return &SSEResponseWriter{
		w:       w,
		flusher: flusher,
	}, nil
}

func (s *SSEResponseWriter) Emit(eventType model.StreamEventType, data any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", eventType, jsonData)
	if err != nil {
		return err
	}

	s.flusher.Flush()
	return nil
}
