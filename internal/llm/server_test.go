package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEnsureOllamaServerReturnsWhenServerIsReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/tags" {
			t.Fatalf("path = %q, want /api/tags", request.URL.Path)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cleanup, err := EnsureOllamaServer(context.Background(), server.URL, time.Second, false)
	if err != nil {
		t.Fatalf("EnsureOllamaServer returned error: %v", err)
	}
	if err := cleanup.Close(); err != nil {
		t.Fatalf("cleanup close: %v", err)
	}
}

func TestEnsureOllamaServerSkipsNonLocalURL(t *testing.T) {
	cleanup, err := EnsureOllamaServer(context.Background(), "http://example.com:11434", time.Millisecond, false)
	if err != nil {
		t.Fatalf("EnsureOllamaServer returned error: %v", err)
	}
	if err := cleanup.Close(); err != nil {
		t.Fatalf("cleanup close: %v", err)
	}
}

func TestEnsureOllamaServerReturnsErrorWhenAutostartIsDisabled(t *testing.T) {
	_, err := EnsureOllamaServer(context.Background(), "http://127.0.0.1:1", time.Millisecond, false)
	if err == nil || !strings.Contains(err.Error(), "autostart is disabled") {
		t.Fatalf("error = %v, want autostart disabled error", err)
	}
}

func TestEnsureOllamaServerReturnsErrorWhenOllamaIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := EnsureOllamaServer(context.Background(), "http://127.0.0.1:1", time.Millisecond, true)
	if err == nil || !strings.Contains(err.Error(), "ollama executable not found") {
		t.Fatalf("error = %v, want missing executable error", err)
	}
}
