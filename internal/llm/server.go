package llm

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

func EnsureOllamaServer(ctx context.Context, baseURL string, timeout time.Duration) (func(), error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse ollama URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("ollama URL must include scheme and host")
	}
	if !isLocalHost(parsed.Hostname()) {
		return func() {}, nil
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	client := &http.Client{Timeout: 500 * time.Millisecond}
	healthURL := strings.TrimRight(parsed.String(), "/") + "/api/tags"
	if ollamaReady(ctx, client, healthURL) {
		return func() {}, nil
	}

	path, err := exec.LookPath("ollama")
	if err != nil {
		return nil, fmt.Errorf("ollama executable not found in PATH: %w", err)
	}

	serverCtx, stop := context.WithCancel(context.Background())
	cmd := exec.CommandContext(serverCtx, path, "serve")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		stop()
		return nil, fmt.Errorf("start ollama serve: %w", err)
	}

	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			stop()
			return nil, fmt.Errorf("wait for ollama startup: %w", ctx.Err())
		case err := <-exited:
			stop()
			if err != nil {
				return nil, fmt.Errorf("ollama serve exited before becoming ready: %w", err)
			}
			return nil, fmt.Errorf("ollama serve exited before becoming ready")
		case <-deadline.C:
			stop()
			return nil, fmt.Errorf("ollama did not become ready within %s", timeout)
		case <-ticker.C:
			if ollamaReady(ctx, client, healthURL) {
				return stop, nil
			}
		}
	}
}

func ollamaReady(ctx context.Context, client *http.Client, healthURL string) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return false
	}

	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()

	return response.StatusCode >= 200 && response.StatusCode < 300
}

func isLocalHost(host string) bool {
	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
