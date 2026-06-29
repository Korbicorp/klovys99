package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGenerateSendsRequestAndParsesResponse(t *testing.T) {
	var received receivedGenerateRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/generate" {
			t.Fatalf("path = %q, want /api/generate", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"response":"{\"entities\":[{\"type\":\"PERSON_NAME\",\"text\":\"Jean Dupont\"}]}","done":true}`)
	}))
	defer server.Close()

	client, err := New(server.URL, 0)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	response, err := client.Generate(context.Background(), "mistral", "prompt", testFormat{Type: "object"})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	if got, want := received.Model, "mistral"; got != want {
		t.Fatalf("model = %v, want %v", got, want)
	}
	if got, want := received.Prompt, "prompt"; got != want {
		t.Fatalf("prompt = %v, want %v", got, want)
	}
	if got, want := received.Stream, false; got != want {
		t.Fatalf("stream = %v, want %v", got, want)
	}
	if got, want := strings.TrimSpace(string(received.Format)), `{"type":"object"}`; got != want {
		t.Fatalf("format = %s, want %s", got, want)
	}
	if got, want := response, `{"entities":[{"type":"PERSON_NAME","text":"Jean Dupont"}]}`; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
}

type receivedGenerateRequest struct {
	Model  string          `json:"model"`
	Prompt string          `json:"prompt"`
	Stream bool            `json:"stream"`
	Format json.RawMessage `json:"format"`
}

type testFormat struct {
	Type string `json:"type"`
}

func TestGenerateReturnsHTTPErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	client, err := New(server.URL, 0)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	_, err = client.Generate(context.Background(), "mistral", "prompt", "json")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %v, want HTTP 500 body", err)
	}
}

func TestGenerateReturnsInvalidResponseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `not-json`)
	}))
	defer server.Close()

	client, err := New(server.URL, 0)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	_, err = client.Generate(context.Background(), "mistral", "prompt", "json")
	if err == nil || !strings.Contains(err.Error(), "parse ollama response") {
		t.Fatalf("error = %v, want parse error", err)
	}
}

func TestNewValidatesBaseURL(t *testing.T) {
	_, err := New("localhost:11434", 0)
	if err == nil || !strings.Contains(err.Error(), "scheme and host") {
		t.Fatalf("error = %v, want URL validation error", err)
	}
}
