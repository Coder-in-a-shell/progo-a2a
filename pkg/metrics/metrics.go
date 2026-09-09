package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// DefaultRegistry is the package-level default metrics registry.
var DefaultRegistry = NewRegistry()

// Registry is a thread-safe collector for Prometheus metrics.
type Registry struct {
	mu sync.RWMutex

	// Requests total counters
	requestsTotal map[requestKey]uint64
	totalRequests uint64

	// Duration summary
	durationSum   map[durationKey]float64
	durationCount map[durationKey]uint64
	totalDurSum   float64
	totalDurCount uint64

	// Active streams gauge
	activeStreams int64

	// Retries counter
	retriesTotal map[string]uint64
	totalRetries uint64

	// Fallback triggered counter
	fallbacksTotal map[fallbackKey]uint64
	totalFallbacks uint64
}

type requestKey struct {
	Method string
	Path   string
	Status int
}

type durationKey struct {
	Method string
	Path   string
}

type fallbackKey struct {
	PrimaryAgent  string
	FallbackAgent string
}

// NewRegistry initializes an empty metrics registry.
func NewRegistry() *Registry {
	return &Registry{
		requestsTotal:  make(map[requestKey]uint64),
		durationSum:    make(map[durationKey]float64),
		durationCount:  make(map[durationKey]uint64),
		retriesTotal:   make(map[string]uint64),
		fallbacksTotal: make(map[fallbackKey]uint64),
	}
}

// IncRequests records an HTTP request with method, path, and status code.
func (r *Registry) IncRequests(method, path string, status int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := requestKey{
		Method: method,
		Path:   path,
		Status: status,
	}
	r.requestsTotal[key]++
	r.totalRequests++
}

// IncRequestsTotal increments the global request count without labels.
func (r *Registry) IncRequestsTotal() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalRequests++
}

// ObserveDuration records the execution duration of a request in seconds.
func (r *Registry) ObserveDuration(method, path string, seconds float64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := durationKey{
		Method: method,
		Path:   path,
	}
	r.durationSum[key] += seconds
	r.durationCount[key]++
	r.totalDurSum += seconds
	r.totalDurCount++
}

// ObserveDurationSeconds records duration globally without labels.
func (r *Registry) ObserveDurationSeconds(seconds float64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalDurSum += seconds
	r.totalDurCount++
}

// IncActiveStreams increments the count of currently active SSE/streaming connections.
func (r *Registry) IncActiveStreams() {
	atomic.AddInt64(&r.activeStreams, 1)
}

// DecActiveStreams decrements the count of currently active SSE/streaming connections.
func (r *Registry) DecActiveStreams() {
	atomic.AddInt64(&r.activeStreams, -1)
}

// GetActiveStreams returns the current number of active streams.
func (r *Registry) GetActiveStreams() int64 {
	return atomic.LoadInt64(&r.activeStreams)
}

// IncRetries records downstream retry attempts, optionally tagged with an agent ID.
func (r *Registry) IncRetries(agentID ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalRetries++
	if len(agentID) > 0 && agentID[0] != "" {
		r.retriesTotal[agentID[0]]++
	}
}

// GetRetries returns the total number of downstream retries.
func (r *Registry) GetRetries() int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return int64(r.totalRetries)
}

// IncFallbackTriggered records fallback agent failover invocations.
func (r *Registry) IncFallbackTriggered(agents ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalFallbacks++
	if len(agents) >= 2 && (agents[0] != "" || agents[1] != "") {
		key := fallbackKey{
			PrimaryAgent:  agents[0],
			FallbackAgent: agents[1],
		}
		r.fallbacksTotal[key]++
	}
}

// GetFallbackTriggered returns the total number of fallback triggers.
func (r *Registry) GetFallbackTriggered() int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return int64(r.totalFallbacks)
}

// Reset clears all counters and gauges in the registry.
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.requestsTotal = make(map[requestKey]uint64)
	r.totalRequests = 0
	r.durationSum = make(map[durationKey]float64)
	r.durationCount = make(map[durationKey]uint64)
	r.totalDurSum = 0
	r.totalDurCount = 0
	atomic.StoreInt64(&r.activeStreams, 0)
	r.retriesTotal = make(map[string]uint64)
	r.totalRetries = 0
	r.fallbacksTotal = make(map[fallbackKey]uint64)
	r.totalFallbacks = 0
}

func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// Gather produces the Prometheus text exposition string.
func (r *Registry) Gather() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var sb strings.Builder

	// 1. a2a_active_streams
	sb.WriteString("# HELP a2a_active_streams Current number of active streaming connections\n")
	sb.WriteString("# TYPE a2a_active_streams gauge\n")
	sb.WriteString(fmt.Sprintf("a2a_active_streams %d\n", atomic.LoadInt64(&r.activeStreams)))

	// 2. a2a_fallback_triggered_total
	sb.WriteString("# HELP a2a_fallback_triggered_total Total number of fallback agent invocations triggered\n")
	sb.WriteString("# TYPE a2a_fallback_triggered_total counter\n")
	if len(r.fallbacksTotal) == 0 {
		sb.WriteString(fmt.Sprintf("a2a_fallback_triggered_total %d\n", r.totalFallbacks))
	} else {
		type fbItem struct {
			key   fallbackKey
			count uint64
		}
		items := make([]fbItem, 0, len(r.fallbacksTotal))
		for k, v := range r.fallbacksTotal {
			items = append(items, fbItem{key: k, count: v})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].key.PrimaryAgent != items[j].key.PrimaryAgent {
				return items[i].key.PrimaryAgent < items[j].key.PrimaryAgent
			}
			return items[i].key.FallbackAgent < items[j].key.FallbackAgent
		})
		for _, item := range items {
			sb.WriteString(fmt.Sprintf("a2a_fallback_triggered_total{fallback=\"%s\",primary=\"%s\"} %d\n",
				escapeLabel(item.key.FallbackAgent), escapeLabel(item.key.PrimaryAgent), item.count))
		}
	}

	// 3. a2a_request_duration_seconds
	sb.WriteString("# HELP a2a_request_duration_seconds HTTP request duration in seconds\n")
	sb.WriteString("# TYPE a2a_request_duration_seconds summary\n")
	if len(r.durationSum) == 0 {
		sb.WriteString(fmt.Sprintf("a2a_request_duration_seconds_sum %.6f\n", r.totalDurSum))
		sb.WriteString(fmt.Sprintf("a2a_request_duration_seconds_count %d\n", r.totalDurCount))
	} else {
		type durItem struct {
			key   durationKey
			sum   float64
			count uint64
		}
		items := make([]durItem, 0, len(r.durationSum))
		for k, sum := range r.durationSum {
			items = append(items, durItem{key: k, sum: sum, count: r.durationCount[k]})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].key.Method != items[j].key.Method {
				return items[i].key.Method < items[j].key.Method
			}
			return items[i].key.Path < items[j].key.Path
		})
		for _, item := range items {
			sb.WriteString(fmt.Sprintf("a2a_request_duration_seconds_sum{method=\"%s\",path=\"%s\"} %.6f\n",
				escapeLabel(item.key.Method), escapeLabel(item.key.Path), item.sum))
			sb.WriteString(fmt.Sprintf("a2a_request_duration_seconds_count{method=\"%s\",path=\"%s\"} %d\n",
				escapeLabel(item.key.Method), escapeLabel(item.key.Path), item.count))
		}
	}

	// 4. a2a_requests_total
	sb.WriteString("# HELP a2a_requests_total Total number of HTTP requests processed\n")
	sb.WriteString("# TYPE a2a_requests_total counter\n")
	if len(r.requestsTotal) == 0 {
		sb.WriteString(fmt.Sprintf("a2a_requests_total %d\n", r.totalRequests))
	} else {
		type reqItem struct {
			key   requestKey
			count uint64
		}
		items := make([]reqItem, 0, len(r.requestsTotal))
		for k, v := range r.requestsTotal {
			items = append(items, reqItem{key: k, count: v})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].key.Method != items[j].key.Method {
				return items[i].key.Method < items[j].key.Method
			}
			if items[i].key.Path != items[j].key.Path {
				return items[i].key.Path < items[j].key.Path
			}
			return items[i].key.Status < items[j].key.Status
		})
		for _, item := range items {
			sb.WriteString(fmt.Sprintf("a2a_requests_total{method=\"%s\",path=\"%s\",status=\"%d\"} %d\n",
				escapeLabel(item.key.Method), escapeLabel(item.key.Path), item.key.Status, item.count))
		}
	}

	// 5. a2a_retries_total
	sb.WriteString("# HELP a2a_retries_total Total number of downstream retries triggered\n")
	sb.WriteString("# TYPE a2a_retries_total counter\n")
	if len(r.retriesTotal) == 0 {
		sb.WriteString(fmt.Sprintf("a2a_retries_total %d\n", r.totalRetries))
	} else {
		keys := make([]string, 0, len(r.retriesTotal))
		for k := range r.retriesTotal {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sb.WriteString(fmt.Sprintf("a2a_retries_total{agent_id=\"%s\"} %d\n", escapeLabel(k), r.retriesTotal[k]))
		}
	}

	return sb.String()
}

// WritePrometheus writes the Prometheus text format directly to an io.Writer.
func (r *Registry) WritePrometheus(w io.Writer) error {
	_, err := io.WriteString(w, r.Gather())
	return err
}

// Handler returns an http.Handler that writes Prometheus text metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, r.Gather())
	})
}
