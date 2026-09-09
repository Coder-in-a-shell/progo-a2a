package main

import (
	"bytes"
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
