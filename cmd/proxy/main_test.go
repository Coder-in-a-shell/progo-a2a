package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/storage"
)

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

	cmd := exec.Command(binPath,
		"-config", "../../config/a2a-proxy.example.yaml",
		"-host", "127.0.0.1",
		"-port", fmt.Sprintf("%d", port),
		"-log-level", "debug",
	)
	cmd.Env = append(os.Environ(),
		"LANGGRAPH_API_KEY=test-langgraph-key",
		"CREWAI_API_TOKEN=test-crewai-token",
		"AUTOGEN_API_KEY=test-autogen-key",
		"OPENAI_API_KEY=test-openai-key",
		"ENTERPRISE_AUTH_TOKEN=test-enterprise-token",
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

	// Verify /a2a/v1/agents endpoint works with auth
	req, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/a2a/v1/agents", port), nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer admin-secret-key-12345")
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
