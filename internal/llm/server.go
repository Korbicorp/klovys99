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

func EnsureOllamaServer(ctx context.Context, baseURL string, timeout time.Duration, autoStart bool) (io.Closer, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse ollama URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("ollama URL must include scheme and host")
	}
	if !isLocalHost(parsed.Hostname()) {
		return noopCloser{}, nil
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	client := &http.Client{Timeout: 500 * time.Millisecond}
	healthURL := strings.TrimRight(parsed.String(), "/") + "/api/tags"
	if ollamaReady(ctx, client, healthURL) {
		return noopCloser{}, nil
	}
	if !autoStart {
		return nil, fmt.Errorf("ollama is not running at %s and autostart is disabled", strings.TrimRight(parsed.String(), "/"))
	}

	path, err := exec.LookPath("ollama")
	if err != nil {
		return nil, fmt.Errorf("ollama executable not found in PATH: %w", err)
	}

	cmd := exec.Command(path, "serve")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ollama serve: %w", err)
	}
	process := &ollamaServerProcess{cmd: cmd}

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
			_ = process.Close()
			return nil, fmt.Errorf("wait for ollama startup: %w", ctx.Err())
		case err := <-exited:
			if err != nil {
				return nil, fmt.Errorf("ollama serve exited before becoming ready: %w", err)
			}
			return nil, fmt.Errorf("ollama serve exited before becoming ready")
		case <-deadline.C:
			_ = process.Close()
			return nil, fmt.Errorf("ollama did not become ready within %s", timeout)
		case <-ticker.C:
			if ollamaReady(ctx, client, healthURL) {
				return process, nil
			}
		}
	}
}

type noopCloser struct{}

func (noopCloser) Close() error {
	return nil
}

type ollamaServerProcess struct {
	cmd *exec.Cmd
}

func (p *ollamaServerProcess) Close() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
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
