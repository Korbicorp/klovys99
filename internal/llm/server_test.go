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

	cleanup, err := EnsureOllamaServer(context.Background(), server.URL, time.Second)
	if err != nil {
		t.Fatalf("EnsureOllamaServer returned error: %v", err)
	}
	cleanup()
}

func TestEnsureOllamaServerSkipsNonLocalURL(t *testing.T) {
	cleanup, err := EnsureOllamaServer(context.Background(), "http://example.com:11434", time.Millisecond)
	if err != nil {
		t.Fatalf("EnsureOllamaServer returned error: %v", err)
	}
	cleanup()
}

func TestEnsureOllamaServerReturnsErrorWhenOllamaIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := EnsureOllamaServer(context.Background(), "http://127.0.0.1:1", time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "ollama executable not found") {
		t.Fatalf("error = %v, want missing executable error", err)
	}
}
