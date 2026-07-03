package proxy_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/Korbicorp/klovys99/internal/detectors"
	"github.com/Korbicorp/klovys99/internal/proxy"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

func TestProxyReplaysClaudeCodeCapturedRequest(t *testing.T) {
	captured := loadJSONFixture[capturedRequest](t, "testdata/claude_code/tmp_request_test.json")
	expected := loadJSONFixture[expectedProxyRequest](t, "testdata/claude_code/tmp_expected_test.json")

	var upstreamCalls int
	var upstreamMethod string
	var upstreamPath string
	var upstreamRawQuery string
	var upstreamHeaders http.Header
	var upstreamBody string

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamCalls++
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}

		upstreamMethod = request.Method
		upstreamPath = request.URL.Path
		upstreamRawQuery = request.URL.RawQuery
		upstreamHeaders = request.Header.Clone()
		upstreamBody = string(body)

		writer.Header().Set("content-type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	logger := zerolog.Nop()
	handler, err := proxy.NewProxyHandler(proxy.Config{
		Target:     mustParseURL(t, upstream.URL),
		Logger:     &logger,
		Anonymizer: anonymizer.NewService(detectors.Default(true)),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	requestURL := server.URL + captured.Path
	if captured.RawQuery != "" {
		requestURL += "?" + captured.RawQuery
	}
	request, err := http.NewRequest(captured.Method, requestURL, strings.NewReader(captured.Body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for name, values := range captured.Headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Header.Set("x-api-key", "test-key")

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}
	if upstreamMethod != expected.Method {
		t.Fatalf("upstream method = %q, want %q", upstreamMethod, expected.Method)
	}
	if upstreamPath != expected.Path {
		t.Fatalf("upstream path = %q, want %q", upstreamPath, expected.Path)
	}
	if upstreamRawQuery != expected.RawQuery {
		t.Fatalf("upstream query = %q, want %q", upstreamRawQuery, expected.RawQuery)
	}
	assertExpectedHeaders(t, upstreamHeaders, expected.Headers)
	assertJSONEqual(t, upstreamBody, expected.Body)

	for _, redactedValue := range expected.RedactedValues {
		if !strings.Contains(captured.Body, redactedValue) {
			t.Fatalf("captured body does not contain redacted fixture value %q", redactedValue)
		}
		if strings.Contains(upstreamBody, redactedValue) {
			t.Fatalf("upstream body contains redacted fixture value %q", redactedValue)
		}
	}
}

type capturedRequest struct {
	Method   string              `json:"method"`
	Path     string              `json:"path"`
	RawQuery string              `json:"raw_query"`
	Headers  map[string][]string `json:"headers"`
	Body     string              `json:"body"`
}

type expectedProxyRequest struct {
	Method         string              `json:"method"`
	Path           string              `json:"path"`
	RawQuery       string              `json:"raw_query"`
	Headers        map[string][]string `json:"headers"`
	Body           string              `json:"body"`
	RedactedValues []string            `json:"redacted_values"`
}

func newTestRouter(handler gin.HandlerFunc) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Any("/*proxyPath", handler)
	return router
}

func loadJSONFixture[T any](t *testing.T, path string) T {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var value T
	if err := json.Unmarshal(content, &value); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", path, err)
	}
	return value
}

func assertExpectedHeaders(t *testing.T, actual http.Header, expected map[string][]string) {
	t.Helper()

	for name, expectedValues := range expected {
		actualValues := actual.Values(name)
		if !reflect.DeepEqual(actualValues, expectedValues) {
			t.Fatalf("upstream header %s = %#v, want %#v", name, actualValues, expectedValues)
		}
	}
}

func assertJSONEqual(t *testing.T, actual string, expected string) {
	t.Helper()

	actualValue := decodeJSONValue(t, "actual body", actual)
	expectedValue := decodeJSONValue(t, "expected body", expected)
	if diff := firstJSONDiff("$", actualValue, expectedValue); diff != "" {
		t.Fatalf("upstream JSON body does not match expected body: %s", diff)
	}
}

func firstJSONDiff(path string, actual any, expected any) string {
	switch expectedTyped := expected.(type) {
	case map[string]any:
		actualTyped, ok := actual.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s actual type %T, want object", path, actual)
		}
		for key, expectedValue := range expectedTyped {
			actualValue, ok := actualTyped[key]
			if !ok {
				return fmt.Sprintf("%s.%s missing", path, key)
			}
			if diff := firstJSONDiff(path+"."+key, actualValue, expectedValue); diff != "" {
				return diff
			}
		}
		for key := range actualTyped {
			if _, ok := expectedTyped[key]; !ok {
				return fmt.Sprintf("%s.%s unexpected", path, key)
			}
		}
		return ""
	case []any:
		actualTyped, ok := actual.([]any)
		if !ok {
			return fmt.Sprintf("%s actual type %T, want array", path, actual)
		}
		if len(actualTyped) != len(expectedTyped) {
			return fmt.Sprintf("%s length = %d, want %d", path, len(actualTyped), len(expectedTyped))
		}
		for index, expectedValue := range expectedTyped {
			if diff := firstJSONDiff(fmt.Sprintf("%s[%d]", path, index), actualTyped[index], expectedValue); diff != "" {
				return diff
			}
		}
		return ""
	default:
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Sprintf("%s = %#v, want %#v", path, actual, expected)
		}
		return ""
	}
}

func decodeJSONValue(t *testing.T, label string, input string) any {
	t.Helper()

	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode %s JSON: %v", label, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("decode %s JSON: trailing content", label)
	}
	return value
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return parsed
}
