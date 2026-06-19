package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestTrafficLogFromEnvDisabledDoesNotCreateProxyLog(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(DebugTrafficLogEnv, "")

	writer, closeLog, err := trafficLogFromEnv()
	if err != nil {
		t.Fatalf("traffic log from env: %v", err)
	}
	if _, err := writer.Write([]byte("request.body={}\n")); err != nil {
		t.Fatalf("write traffic log: %v", err)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("close traffic log: %v", err)
	}
	if _, err := os.Stat(DefaultLogPath); !os.IsNotExist(err) {
		t.Fatalf("proxy log stat error = %v, want not exist", err)
	}
}

func TestTrafficLogFromEnvEnabledWritesProxyLog(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(DebugTrafficLogEnv, "1")

	writer, closeLog, err := trafficLogFromEnv()
	if err != nil {
		t.Fatalf("traffic log from env: %v", err)
	}
	if _, err := writer.Write([]byte("request.body={}\n")); err != nil {
		t.Fatalf("write traffic log: %v", err)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("close traffic log: %v", err)
	}

	logContent, err := os.ReadFile(DefaultLogPath)
	if err != nil {
		t.Fatalf("read proxy log: %v", err)
	}
	if got, want := string(logContent), "request.body={}\n"; got != want {
		t.Fatalf("proxy log = %q, want %q", got, want)
	}
}

func TestProxyForwardsRequestAndResponse(t *testing.T) {
	var upstreamMethod string
	var upstreamPath string
	var upstreamRawQuery string
	var upstreamHost string
	var upstreamHeader string
	var upstreamBody string

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}

		upstreamMethod = request.Method
		upstreamPath = request.URL.Path
		upstreamRawQuery = request.URL.RawQuery
		upstreamHost = request.Host
		upstreamHeader = request.Header.Get("anthropic-version")
		upstreamBody = string(body)

		writer.Header().Set("x-upstream", "ok")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"id":"msg_123","content":"hello"}`))
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	handler, err := NewHandler(Config{
		Target: target,
		Logger: &logger,
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	requestBody := `{"model":"claude","messages":[{"role":"user","content":"secret prompt"}]}`
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages?x=1", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("x-api-key", "test-key")

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	if upstreamMethod != http.MethodPost {
		t.Fatalf("upstream method = %q, want %q", upstreamMethod, http.MethodPost)
	}
	if upstreamPath != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages", upstreamPath)
	}
	if upstreamRawQuery != "x=1" {
		t.Fatalf("upstream query = %q, want x=1", upstreamRawQuery)
	}
	if upstreamHost != target.Host {
		t.Fatalf("upstream host = %q, want %q", upstreamHost, target.Host)
	}
	if upstreamHeader != "2023-06-01" {
		t.Fatalf("upstream anthropic-version = %q, want 2023-06-01", upstreamHeader)
	}
	if upstreamBody != requestBody {
		t.Fatalf("upstream body = %q, want %q", upstreamBody, requestBody)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	if response.Header.Get("x-upstream") != "ok" {
		t.Fatalf("response x-upstream = %q, want ok", response.Header.Get("x-upstream"))
	}
	if got, want := string(responseBody), `{"id":"msg_123","content":"hello"}`; got != want {
		t.Fatalf("response body = %q, want %q", got, want)
	}

	logOutput := logs.String()
	if logOutput != "" {
		t.Fatalf("logs = %q, want no logs without session prompt", logOutput)
	}
}

func TestSessionPromptAnonymizerLogsOnlyStats(t *testing.T) {
	body := []byte(`{"model":"claude","messages":[{"role":"user","content":[{"type":"text","text":"before <session>Email: alice@example.com\nTel: 06 12 34 56 78</session> after","cache_control":{"type":"ephemeral"}}]}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	anonymizedBody := string(newSessionPromptAnonymizer().anonymize(logger, body))
	logOutput := logs.String()

	if strings.Contains(anonymizedBody, "alice@example.com") {
		t.Fatalf("body = %q, want email anonymized", anonymizedBody)
	}
	if !strings.Contains(anonymizedBody, `<session>Email: [EMAIL_1]\nTel: [PHONE_1]</session>`) {
		t.Fatalf("body = %q, want anonymized session content", anonymizedBody)
	}
	if !strings.Contains(logOutput, `"EMAIL":1`) || !strings.Contains(logOutput, `"PHONE":1`) {
		t.Fatalf("logs = %q, want anonymized stats", logOutput)
	}
	for _, unexpected := range []string{"alice@example.com", "06 12 34 56 78", "[EMAIL_1]", "[PHONE_1]", "prompt_original", "prompt_anonymized"} {
		if strings.Contains(logOutput, unexpected) {
			t.Fatalf("logs = %q, did not want %q", logOutput, unexpected)
		}
	}
}

func TestSessionPromptAnonymizerFindsPromptsInsideSystemPrompts(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"rules <session>Contact alice@example.com</session> keep alice@example.com"}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	anonymizedBody := string(newSessionPromptAnonymizer().anonymize(logger, body))
	logOutput := logs.String()

	if !strings.Contains(anonymizedBody, `rules <session>Contact [EMAIL_1]</session> keep alice@example.com`) {
		t.Fatalf("body = %q, want only session content anonymized", anonymizedBody)
	}
	if !strings.Contains(logOutput, `"EMAIL":1`) {
		t.Fatalf("logs = %q, want anonymized stats", logOutput)
	}
	for _, unexpected := range []string{"alice@example.com", "[EMAIL_1]", "prompt_original", "prompt_anonymized"} {
		if strings.Contains(logOutput, unexpected) {
			t.Fatalf("logs = %q, did not want %q", logOutput, unexpected)
		}
	}
}

func TestSessionPromptAnonymizerAnonymizesUserContentOutsideSession(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>internal context</system-reminder>"},{"type":"text","text":"Donne moi l'IBAN FR76 3000 6000 0112 3456 7890 189"}]}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	anonymizedBody := string(newSessionPromptAnonymizer().anonymize(logger, body))
	logOutput := logs.String()

	if strings.Contains(anonymizedBody, "FR76 3000 6000 0112 3456 7890 189") {
		t.Fatalf("body = %q, want user content IBAN anonymized", anonymizedBody)
	}
	if !strings.Contains(anonymizedBody, "Donne moi l'IBAN [IBAN_1]") {
		t.Fatalf("body = %q, want anonymized user content", anonymizedBody)
	}
	if !strings.Contains(logOutput, `"IBAN":1`) {
		t.Fatalf("logs = %q, want IBAN stats", logOutput)
	}
	for _, unexpected := range []string{"FR76 3000 6000 0112 3456 7890 189", "[IBAN_1]", "prompt_original", "prompt_anonymized"} {
		if strings.Contains(logOutput, unexpected) {
			t.Fatalf("logs = %q, did not want %q", logOutput, unexpected)
		}
	}
}

func TestSessionPromptAnonymizerLogsMultipleSessionsWithStableTokens(t *testing.T) {
	body := []byte(`{"text":"<session>Email: alice@example.com</session> mid <session>Email: bob@example.com and alice@example.com</session>"}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	anonymizedBody := string(newSessionPromptAnonymizer().anonymize(logger, body))
	logOutput := logs.String()

	if !strings.Contains(anonymizedBody, `<session>Email: [EMAIL_1]</session> mid <session>Email: [EMAIL_2] and [EMAIL_1]</session>`) {
		t.Fatalf("body = %q, want multiple sessions anonymized with stable tokens", anonymizedBody)
	}
	if !strings.Contains(logOutput, `"EMAIL":3`) {
		t.Fatalf("logs = %q, want aggregated anonymized stats", logOutput)
	}
	for _, unexpected := range []string{"alice@example.com", "bob@example.com", "[EMAIL_1]", "[EMAIL_2]", "prompt_original", "prompt_anonymized"} {
		if strings.Contains(logOutput, unexpected) {
			t.Fatalf("logs = %q, did not want %q", logOutput, unexpected)
		}
	}
}

func TestSessionPromptAnonymizerDoesNotChangeNonUserBodyWithoutSession(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"alice@example.com outside session"}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	anonymizedBody := newSessionPromptAnonymizer().anonymize(logger, body)

	if logs.Len() != 0 {
		t.Fatalf("logs = %q, want empty logs without session prompt", logs.String())
	}
	if !bytes.Equal(anonymizedBody, body) {
		t.Fatalf("body = %q, want original body", string(anonymizedBody))
	}
}

func TestSessionPromptAnonymizerKeepsTokensAcrossCalls(t *testing.T) {
	promptAnonymizer := newSessionPromptAnonymizer()
	var logs bytes.Buffer
	logger := zerolog.New(&logs)

	promptAnonymizer.anonymize(logger, []byte(`{"text":"<session>Email: alice@example.com</session>"}`))
	second := string(promptAnonymizer.anonymize(logger, []byte(`{"text":"<session>Email: bob@example.com</session>"}`)))

	if !strings.Contains(second, `<session>Email: [EMAIL_2]</session>`) {
		t.Fatalf("body = %q, want token state preserved across calls", second)
	}
	if !strings.Contains(logs.String(), `"EMAIL":1`) {
		t.Fatalf("logs = %q, want anonymized stats", logs.String())
	}
	for _, unexpected := range []string{"alice@example.com", "bob@example.com", "[EMAIL_1]", "[EMAIL_2]", "prompt_original", "prompt_anonymized"} {
		if strings.Contains(logs.String(), unexpected) {
			t.Fatalf("logs = %q, did not want %q", logs.String(), unexpected)
		}
	}
}

func TestProxyForwardsAnonymizedSessionPrompts(t *testing.T) {
	var upstreamBody string
	responseBody := []byte(`{"content":"visible response"}`)
	var compressedResponse bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressedResponse)
	if _, err := gzipWriter.Write(responseBody); err != nil {
		t.Fatalf("compress response body: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamBody = string(body)

		writer.Header().Set("Content-Encoding", "gzip")
		writer.Header().Set("x-upstream", "ok")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(compressedResponse.Bytes())
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}

	var logs bytes.Buffer
	var trafficLogs bytes.Buffer
	logger := zerolog.New(&logs)
	handler, err := NewHandler(Config{
		Target:     target,
		Logger:     &logger,
		TrafficLog: &trafficLogs,
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(`{"messages":[{"role":"user","content":[{"type":"text","text":"before <session>Email: alice@example.com</session> after"},{"type":"text","text":"IBAN FR76 3000 6000 0112 3456 7890 189"}]}]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read proxy response body: %v", err)
	}

	if strings.Contains(upstreamBody, "alice@example.com") || strings.Contains(upstreamBody, "FR76 3000 6000 0112 3456 7890 189") {
		t.Fatalf("upstream body = %q, want original prompt values anonymized", upstreamBody)
	}
	if !strings.Contains(upstreamBody, `<session>Email: [EMAIL_1]</session>`) {
		t.Fatalf("upstream body = %q, want anonymized session prompt", upstreamBody)
	}
	if !strings.Contains(upstreamBody, `IBAN [IBAN_1]`) {
		t.Fatalf("upstream body = %q, want anonymized user content", upstreamBody)
	}

	trafficOutput := trafficLogs.String()
	if strings.Contains(trafficOutput, "alice@example.com") || strings.Contains(trafficOutput, "FR76 3000 6000 0112 3456 7890 189") {
		t.Fatalf("traffic logs = %q, want final anonymized request only", trafficOutput)
	}
	if !strings.Contains(trafficOutput, `<session>Email: [EMAIL_1]</session>`) || !strings.Contains(trafficOutput, `IBAN [IBAN_1]`) {
		t.Fatalf("traffic logs = %q, want final request body", trafficOutput)
	}
	if !strings.Contains(trafficOutput, `response.body={"content":"visible response"}`) {
		t.Fatalf("traffic logs = %q, want decoded gzip response body", trafficOutput)
	}

	logOutput := logs.String()
	if !strings.Contains(logOutput, `"EMAIL":1`) || !strings.Contains(logOutput, `"IBAN":1`) {
		t.Fatalf("logs = %q, want anonymized stats", logOutput)
	}
	for _, unexpected := range []string{"request.", "response.", "upstream.", "before", "after", "visible response", "alice@example.com", "FR76 3000 6000 0112 3456 7890 189", "[EMAIL_1]", "[IBAN_1]", "prompt_original", "prompt_anonymized"} {
		if strings.Contains(logOutput, unexpected) {
			t.Fatalf("logs = %q, did not want %q", logOutput, unexpected)
		}
	}
}
