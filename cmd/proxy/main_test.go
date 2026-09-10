package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/storage"
)

func writeTestConfig(t *testing.T, storageBackend string, extraYAML string) string {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	storageYAML := `storage:
  backend: memory
`
	if storageBackend == "postgres" {
		storageYAML = `storage:
  backend: postgres
  postgres:
    dsn: "postgres://user:pass@localhost:5432/testdb"
`
	}

	content := fmt.Sprintf(`server:
  host: "127.0.0.1"
  port: 8080
security:
  enabled: false
agents:
  - id: "test-agent"
    name: "Test Agent"
    type: "openai"
    endpoint: "http://127.0.0.1:8080/dummy"
    auth:
      type: "none"
%s
%s
`, storageYAML, extraYAML)

	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test config file: %v", err)
	}
	return configPath
}

func TestBinaryHelpFlag(t *testing.T) {
	cmd := exec.Command("go", "run", "main.go", "-help")
	out, _ := cmd.CombinedOutput()
	outStr := string(out)
	if len(outStr) == 0 {
		t.Fatalf("expected help output, got empty")
	}
	if !strings.Contains(outStr, "-config") {
		t.Errorf("expected -config flag in help output, got: %s", outStr)
	}
	if !strings.Contains(outStr, "-host") {
		t.Errorf("expected -host flag in help output, got: %s", outStr)
	}
	if !strings.Contains(outStr, "-port") {
		t.Errorf("expected -port flag in help output, got: %s", outStr)
	}
	if !strings.Contains(outStr, "-log-level") {
		t.Errorf("expected -log-level flag in help output, got: %s", outStr)
	}
	if !strings.Contains(outStr, "-role") {
		t.Errorf("expected -role flag in help output, got: %s", outStr)
	}
}

func TestBinaryBuild(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "a2a-proxy-build-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	binPath := filepath.Join(tmpDir, "a2a-proxy")
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v, output: %s", err, string(out))
	}

	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		t.Fatalf("expected binary at %s, but file does not exist", binPath)
	}

	// Verify built binary runs directly with -help
	helpCmd := exec.Command(binPath, "-help")
	helpOut, _ := helpCmd.CombinedOutput()
	if !strings.Contains(string(helpOut), "-config") {
		t.Errorf("built binary failed help output test: %s", string(helpOut))
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"WARN", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"unknown", slog.LevelInfo},
		{"", slog.LevelInfo},
	}

	for _, tc := range tests {
		got := parseLogLevel(tc.input)
		if got != tc.expected {
			t.Errorf("parseLogLevel(%q) = %v, expected %v", tc.input, got, tc.expected)
		}
	}
}

func TestServerRunAndGracefulShutdown(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "a2a-proxy-run-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	binPath := filepath.Join(tmpDir, "a2a-proxy")
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v: %s", err, string(out))
	}

	// Find free port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	testCfg := writeTestConfig(t, "memory", "")

	cmd := exec.Command(binPath,
		"-config", testCfg,
		"-host", "127.0.0.1",
		"-port", fmt.Sprintf("%d", port),
		"-log-level", "debug",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start server binary: %v", err)
	}

	// Poll healthz
	client := &http.Client{Timeout: 1 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	ready := false
	for i := 0; i < 40; i++ {
		time.Sleep(100 * time.Millisecond)
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
	}

	if !ready {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("server did not become healthy in time. Stdout: %s, Stderr: %s", stdout.String(), stderr.String())
	}

	// Verify /a2a/v1/agents endpoint works
	req, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/a2a/v1/agents", port), nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	agentsResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to get agents: %v", err)
	}
	agentsResp.Body.Close()
	if agentsResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from agents endpoint, got %d", agentsResp.StatusCode)
	}

	// Send Interrupt signal to trigger graceful shutdown
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("failed to send SIGINT: %v", err)
	}

	// Wait for server to exit
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Wait()
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("server exited with error: %v. Output: %s", err, stdout.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("server timed out during graceful shutdown")
	}

	outStr := stdout.String()
	if !strings.Contains(outStr, "server exited cleanly") && !strings.Contains(outStr, "shutting down gracefully") {
		t.Errorf("expected graceful shutdown log message, got: %s", outStr)
	}
}

func TestBuildTaskStore_MemoryCapacity(t *testing.T) {
	ctx := context.Background()

	t.Run("custom memory capacity bounds storage", func(t *testing.T) {
		cfg := &config.Config{
			Storage: config.StorageConfig{
				Backend: "memory",
				Memory: config.MemoryStorageConfig{
					MaxTasks: 3,
				},
			},
		}

		store, closeFn, err := buildTaskStore(cfg)
		if err != nil {
			t.Fatalf("buildTaskStore failed: %v", err)
		}
		if store == nil {
			t.Fatal("expected non-nil store")
		}
		if closeFn == nil {
			t.Fatal("expected non-nil closeFn")
		}
		defer closeFn()

		memStore, ok := store.(*storage.MemoryTaskStore)
		if !ok {
			t.Fatalf("expected *storage.MemoryTaskStore, got %T", store)
		}

		for i := 1; i <= 3; i++ {
			taskID := fmt.Sprintf("task-%d", i)
			err := memStore.Save(ctx, &model.TaskResponse{
				TaskID:  taskID,
				AgentID: "agent-1",
				Status:  model.StatusCompleted,
			})
			if err != nil {
				t.Fatalf("save %s failed: %v", taskID, err)
			}
		}

		if memStore.Len() != 3 {
			t.Errorf("expected len 3, got %d", memStore.Len())
		}

		// Save 4th task, should evict task-1 (oldest FIFO)
		err = memStore.Save(ctx, &model.TaskResponse{
			TaskID:  "task-4",
			AgentID: "agent-1",
			Status:  model.StatusCompleted,
		})
		if err != nil {
			t.Fatalf("save task-4 failed: %v", err)
		}

		if memStore.Len() != 3 {
			t.Errorf("expected len 3 after eviction, got %d", memStore.Len())
		}

		// task-1 should be evicted
		_, err = memStore.Load(ctx, "task-1")
		if err != storage.ErrTaskNotFound {
			t.Errorf("expected ErrTaskNotFound for task-1, got %v", err)
		}

		// task-4 should exist
		resp, err := memStore.Load(ctx, "task-4")
		if err != nil {
			t.Errorf("expected task-4 to be found, got %v", err)
		}
		if resp == nil || resp.TaskID != "task-4" {
			t.Errorf("unexpected task response: %+v", resp)
		}
	})

	t.Run("zero max_tasks defaults to DefaultMaxTasks", func(t *testing.T) {
		cfg := &config.Config{
			Storage: config.StorageConfig{
				Backend: "memory",
				Memory: config.MemoryStorageConfig{
					MaxTasks: 0,
				},
			},
		}

		store, closeFn, err := buildTaskStore(cfg)
		if err != nil {
			t.Fatalf("buildTaskStore failed: %v", err)
		}
		defer closeFn()

		memStore, ok := store.(*storage.MemoryTaskStore)
		if !ok {
			t.Fatalf("expected *storage.MemoryTaskStore, got %T", store)
		}

		err = memStore.Save(ctx, &model.TaskResponse{
			TaskID:  "task-default",
			AgentID: "agent-1",
			Status:  model.StatusCompleted,
		})
		if err != nil {
			t.Fatalf("save failed: %v", err)
		}

		resp, err := memStore.Load(ctx, "task-default")
		if err != nil || resp.TaskID != "task-default" {
			t.Errorf("expected task-default to load, got %v", err)
		}
	})
}

func TestBuildTaskStore_NoOpClose(t *testing.T) {
	cfg := &config.Config{
		Storage: config.StorageConfig{
			Backend: "memory",
		},
	}

	store, closeFn, err := buildTaskStore(cfg)
	if err != nil {
		t.Fatalf("buildTaskStore failed: %v", err)
	}

	// Calling closeFn multiple times must be safe and idempotent
	closeFn()
	closeFn()

	// Store should remain functional
	err = store.Save(context.Background(), &model.TaskResponse{
		TaskID:  "task-after-close",
		AgentID: "agent-1",
		Status:  model.StatusCompleted,
	})
	if err != nil {
		t.Errorf("expected store to work after memory close, got %v", err)
	}
}

func TestBuildTaskStore_ErrorsAndSanitization(t *testing.T) {
	t.Run("nil config returns error", func(t *testing.T) {
		_, _, err := buildTaskStore(nil)
		if err == nil {
			t.Fatal("expected error for nil config, got nil")
		}
	})

	t.Run("unsupported backend returns error", func(t *testing.T) {
		cfg := &config.Config{
			Storage: config.StorageConfig{
				Backend: "unsupported-storage",
			},
		}
		_, _, err := buildTaskStore(cfg)
		if err == nil {
			t.Fatal("expected error for unsupported backend, got nil")
		}
		if !strings.Contains(err.Error(), "unsupported storage backend") {
			t.Errorf("expected error mentioning unsupported storage backend, got: %s", err.Error())
		}
	})

	t.Run("sanitizeStorageError redacts DSN and password", func(t *testing.T) {
		secretDSN := "postgres://admin_user:ultra_secret_pw@10.0.0.1:5432/proddb"
		rawErr := fmt.Errorf("failed connecting to %s: connection refused", secretDSN)
		sanitized := sanitizeStorageError(rawErr, secretDSN)

		if strings.Contains(sanitized.Error(), "ultra_secret_pw") {
			t.Errorf("sanitized error leaked password: %s", sanitized.Error())
		}
		if strings.Contains(sanitized.Error(), secretDSN) {
			t.Errorf("sanitized error leaked raw DSN: %s", sanitized.Error())
		}
		if !strings.Contains(sanitized.Error(), "[REDACTED]") {
			t.Errorf("sanitized error missing [REDACTED]: %s", sanitized.Error())
		}
	})
}

func TestRun_OrchestrationAndRoles(t *testing.T) {
	t.Run("api role startup and graceful shutdown", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to listen on free port: %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		testCfg := writeTestConfig(t, "memory", "")

		var stdout, stderr bytes.Buffer
		errCh := make(chan error, 1)
		go func() {
			errCh <- run(ctx, []string{
				"-config", testCfg,
				"-host", "127.0.0.1",
				"-port", fmt.Sprintf("%d", port),
				"-role", "api",
				"-log-level", "debug",
			}, &stdout, &stderr)
		}()

		// Poll healthz
		client := &http.Client{Timeout: 500 * time.Millisecond}
		url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
		ready := false
		for i := 0; i < 40; i++ {
			time.Sleep(50 * time.Millisecond)
			resp, err := client.Get(url)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					ready = true
					break
				}
			}
		}

		if !ready {
			cancel()
			t.Fatalf("server failed to start on port %d, stderr: %s", port, stderr.String())
		}

		// Trigger in-process graceful shutdown
		cancel()

		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("run returned error on graceful shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for in-process graceful shutdown")
		}
	})

	t.Run("missing config file returns error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), []string{"-config", "nonexistent.yaml"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected error for nonexistent config, got nil")
		}
	})

	t.Run("invalid flag returns error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), []string{"-unsupported-flag-xyz"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected error for unsupported flag, got nil")
		}
	})

	t.Run("invalid role override returns error", func(t *testing.T) {
		testCfg := writeTestConfig(t, "memory", "")
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), []string{
			"-config", testCfg,
			"-role", "invalid-role",
		}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected error for invalid role, got nil")
		}
		if !strings.Contains(err.Error(), "must be one of api, worker, all") {
			t.Errorf("expected role error message, got: %v", err)
		}
	})

	t.Run("worker role with memory storage returns error", func(t *testing.T) {
		testCfg := writeTestConfig(t, "memory", "")
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), []string{
			"-config", testCfg,
			"-role", "worker",
		}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected error for worker role with memory storage, got nil")
		}
		if !strings.Contains(err.Error(), "requires postgres storage backend") {
			t.Errorf("expected storage requirement error, got: %v", err)
		}
	})

	t.Run("all role with memory storage returns error", func(t *testing.T) {
		testCfg := writeTestConfig(t, "memory", "")
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), []string{
			"-config", testCfg,
			"-role", "all",
		}, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected error for all role with memory storage, got nil")
		}
		if !strings.Contains(err.Error(), "requires postgres storage backend") {
			t.Errorf("expected storage requirement error, got: %v", err)
		}
	})
}

type fakeServerComponent struct {
	startFunc    func() error
	shutdownFunc func(ctx context.Context) error
}

func (f *fakeServerComponent) Start() error {
	if f.startFunc != nil {
		return f.startFunc()
	}
	return nil
}

func (f *fakeServerComponent) Shutdown(ctx context.Context) error {
	if f.shutdownFunc != nil {
		return f.shutdownFunc(ctx)
	}
	return nil
}

type fakeWorkerComponent struct {
	runFunc func(ctx context.Context) error
}

func (f *fakeWorkerComponent) Run(ctx context.Context) error {
	if f.runFunc != nil {
		return f.runFunc(ctx)
	}
	return nil
}

func TestRunAllComponents_CompletionSignaling(t *testing.T) {
	t.Run("normal shutdown does not wait for drain timeout", func(t *testing.T) {
		serverClosed := make(chan struct{})
		srv := &fakeServerComponent{
			startFunc: func() error {
				<-serverClosed
				return http.ErrServerClosed
			},
			shutdownFunc: func(ctx context.Context) error {
				close(serverClosed)
				return nil
			},
		}

		eng := &fakeWorkerComponent{
			runFunc: func(ctx context.Context) error {
				<-ctx.Done()
				return nil // normal clean exit
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		start := time.Now()

		go func() {
			// drain timeout configured as 30s; normal shutdown must NOT wait 35s!
			errCh <- runAllComponents(ctx, srv, eng, 30, nil)
		}()

		time.Sleep(20 * time.Millisecond)
		cancel() // trigger shutdown

		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("expected nil error on normal shutdown, got: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("normal shutdown waited longer than 2s; completion signaling is blocked or waited for drain timeout")
		}

		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("normal shutdown took %v, expected < 2s", elapsed)
		}
	})

	t.Run("unexpected server error propagates immediately", func(t *testing.T) {
		srv := &fakeServerComponent{
			startFunc: func() error {
				return errors.New("port already bound")
			},
			shutdownFunc: func(ctx context.Context) error {
				return nil
			},
		}

		var workerCanceled atomic.Bool
		eng := &fakeWorkerComponent{
			runFunc: func(ctx context.Context) error {
				<-ctx.Done()
				workerCanceled.Store(true)
				return nil
			},
		}

		ctx := context.Background()
		err := runAllComponents(ctx, srv, eng, 5, nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "port already bound") {
			t.Errorf("expected error containing 'port already bound', got: %v", err)
		}
		if !workerCanceled.Load() {
			t.Error("expected worker context to be canceled when server failed")
		}
	})

	t.Run("unexpected server nil exit propagates", func(t *testing.T) {
		srv := &fakeServerComponent{
			startFunc: func() error {
				return nil // unexpected early exit with nil
			},
			shutdownFunc: func(ctx context.Context) error {
				return nil
			},
		}

		eng := &fakeWorkerComponent{
			runFunc: func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			},
		}

		err := runAllComponents(context.Background(), srv, eng, 5, nil)
		if err == nil {
			t.Fatal("expected error for unexpected server nil exit, got nil")
		}
		if !strings.Contains(err.Error(), "server exited unexpectedly") {
			t.Errorf("expected server exited unexpectedly error, got: %v", err)
		}
	})

	t.Run("unexpected worker error propagates immediately", func(t *testing.T) {
		serverClosed := make(chan struct{})
		var serverShutdownCalled atomic.Bool
		srv := &fakeServerComponent{
			startFunc: func() error {
				<-serverClosed
				return http.ErrServerClosed
			},
			shutdownFunc: func(ctx context.Context) error {
				serverShutdownCalled.Store(true)
				close(serverClosed)
				return nil
			},
		}

		eng := &fakeWorkerComponent{
			runFunc: func(ctx context.Context) error {
				return errors.New("fatal database connection failure")
			},
		}

		err := runAllComponents(context.Background(), srv, eng, 5, nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "fatal database connection failure") {
			t.Errorf("expected error containing 'fatal database connection failure', got: %v", err)
		}
		if !serverShutdownCalled.Load() {
			t.Error("expected server shutdown to be invoked when worker failed")
		}
	})

	t.Run("unexpected worker nil exit propagates", func(t *testing.T) {
		serverClosed := make(chan struct{})
		srv := &fakeServerComponent{
			startFunc: func() error {
				<-serverClosed
				return http.ErrServerClosed
			},
			shutdownFunc: func(ctx context.Context) error {
				close(serverClosed)
				return nil
			},
		}

		eng := &fakeWorkerComponent{
			runFunc: func(ctx context.Context) error {
				return nil // unexpected early worker exit with nil
			},
		}

		err := runAllComponents(context.Background(), srv, eng, 5, nil)
		if err == nil {
			t.Fatal("expected error for unexpected worker nil exit, got nil")
		}
		if !strings.Contains(err.Error(), "worker exited unexpectedly") {
			t.Errorf("expected worker exited unexpectedly error, got: %v", err)
		}
	})
}

func TestBuildStorageHelpers_Validation(t *testing.T) {
	t.Run("nil config returns error for JobRepository", func(t *testing.T) {
		_, _, err := buildJobRepository(nil)
		if err == nil {
			t.Fatal("expected error for nil config, got nil")
		}
	})

	t.Run("nil config returns error for StorageBundle", func(t *testing.T) {
		_, _, err := buildStorageBundle(nil)
		if err == nil {
			t.Fatal("expected error for nil config, got nil")
		}
	})

	t.Run("blank DSN returns error for JobRepository", func(t *testing.T) {
		cfg := &config.Config{
			Storage: config.StorageConfig{
				Backend: "postgres",
				Postgres: config.PostgresStorageConfig{
					DSN: "",
				},
			},
		}
		_, _, err := buildJobRepository(cfg)
		if err == nil {
			t.Fatal("expected error for blank DSN, got nil")
		}
	})

	t.Run("blank DSN returns error for StorageBundle", func(t *testing.T) {
		cfg := &config.Config{
			Storage: config.StorageConfig{
				Backend: "postgres",
				Postgres: config.PostgresStorageConfig{
					DSN: "",
				},
			},
		}
		_, _, err := buildStorageBundle(cfg)
		if err == nil {
			t.Fatal("expected error for blank DSN, got nil")
		}
	})
}
