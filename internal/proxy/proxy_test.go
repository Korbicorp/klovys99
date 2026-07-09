package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/Korbicorp/klovys99/internal/detectors"
	"github.com/Korbicorp/klovys99/internal/proxy/providers"
	statlog "github.com/Korbicorp/klovys99/internal/stats"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

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
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	handler, err := NewProxyHandler(Config{
		Target:     target,
		Logger:     &logger,
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
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
	expectedUpstreamBody := `{"model":"claude","messages":[{"role":"user","content":"secret prompt"}],"stream":false}`
	if diff := firstJSONDiff("$", decodeJSONValue(t, "upstream body", upstreamBody), decodeJSONValue(t, "expected upstream body", expectedUpstreamBody)); diff != "" {
		t.Fatalf("upstream JSON body does not match expected body: %s", diff)
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

func TestProxyRoutesConfiguredPrefixesToDifferentUpstreams(t *testing.T) {
	var anthropicPath string
	var openAIPath string

	anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		anthropicPath = request.URL.Path
		writer.WriteHeader(http.StatusOK)
	}))
	defer anthropicUpstream.Close()

	openAIUpstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		openAIPath = request.URL.Path
		writer.WriteHeader(http.StatusOK)
	}))
	defer openAIUpstream.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, anthropicUpstream.URL),
		RouteTargets: map[string]*url.URL{
			AnthropicRoutePrefix: mustParseURL(t, anthropicUpstream.URL),
			OpenAIRoutePrefix:    mustParseURL(t, openAIUpstream.URL),
		},
		Logger:     ptrLogger(zerolog.Nop()),
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	anthropicResponse, err := http.Post(server.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatalf("call anthropic route: %v", err)
	}
	anthropicResponse.Body.Close()

	openAIResponse, err := http.Post(server.URL+"/openai/v1/responses", "application/json", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("call openai route: %v", err)
	}
	openAIResponse.Body.Close()

	if anthropicPath != "/v1/messages" {
		t.Fatalf("anthropic path = %q, want /v1/messages", anthropicPath)
	}
	if openAIPath != "/v1/responses" {
		t.Fatalf("openai path = %q, want /v1/responses", openAIPath)
	}
}

func TestProxyAnonymizesPrefixedAnthropicMessagesRequests(t *testing.T) {
	var upstreamPath string
	var upstreamBody string

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamPath = request.URL.Path
		upstreamBody = string(body)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, upstream.URL),
		RouteTargets: map[string]*url.URL{
			AnthropicRoutePrefix: mustParseURL(t, upstream.URL),
		},
		Logger:     &logger,
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"Email alice@example.com"}]}`))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	if upstreamPath != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages", upstreamPath)
	}
	if strings.Contains(upstreamBody, "alice@example.com") {
		t.Fatalf("upstream body = %q, want prefixed Anthropic prompt anonymized", upstreamBody)
	}
	if !strings.Contains(upstreamBody, "Email [EMAIL_1]") {
		t.Fatalf("upstream body = %q, want email token", upstreamBody)
	}
	if !strings.Contains(logs.String(), `"EMAIL":1`) {
		t.Fatalf("logs = %q, want anonymized stats", logs.String())
	}
}

func TestProxyForcesNonStreamingAnthropicMessagesRequests(t *testing.T) {
	var upstreamBody string

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamBody = string(body)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, upstream.URL),
		RouteTargets: map[string]*url.URL{
			AnthropicRoutePrefix: mustParseURL(t, upstream.URL),
		},
		Logger:     ptrLogger(zerolog.Nop()),
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/anthropic/v1/messages", "application/json", strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	response.Body.Close()

	if !strings.Contains(upstreamBody, `"stream":false`) {
		t.Fatalf("upstream body = %q, want stream forced to false", upstreamBody)
	}
}

func TestProxyDeanonymizesAnthropicResponses(t *testing.T) {
	var upstreamBody string

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamBody = string(body)
		token := extractToken(t, upstreamBody, `Email ([^"]+)`)
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(fmt.Sprintf(`{"content":[{"type":"text","text":"Response for %s"}]}`, token)))
	}))
	defer upstream.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, upstream.URL),
		Logger: ptrLogger(zerolog.Nop()),
		Anonymizer: anonymizer.NewService([]anonymizer.Detector{
			literalDetector{entityType: anonymizer.EntityEmail, value: "alice@example.com"},
		}),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"Email alice@example.com"}]}`))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read proxy response body: %v", err)
	}
	if strings.Contains(upstreamBody, "alice@example.com") {
		t.Fatalf("upstream body = %q, want anonymized request", upstreamBody)
	}
	if got := string(body); !strings.Contains(got, "alice@example.com") {
		t.Fatalf("response body = %q, want restored email", got)
	}
}

func TestProxyDeanonymizesGzippedAnthropicResponses(t *testing.T) {
	var upstreamBody string
	var upstreamAcceptEncoding string

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamBody = string(body)
		upstreamAcceptEncoding = request.Header.Get("Accept-Encoding")
		token := extractToken(t, upstreamBody, `Email ([^"]+)`)

		var compressed bytes.Buffer
		gzipWriter := gzip.NewWriter(&compressed)
		if _, err := gzipWriter.Write([]byte(fmt.Sprintf(`{"content":[{"type":"text","text":"Response for %s"}]}`, token))); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
		if err := gzipWriter.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}

		writer.Header().Set("content-type", "application/json")
		writer.Header().Set("content-encoding", "gzip")
		_, _ = writer.Write(compressed.Bytes())
	}))
	defer upstream.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, upstream.URL),
		Logger: ptrLogger(zerolog.Nop()),
		Anonymizer: anonymizer.NewService([]anonymizer.Detector{
			literalDetector{entityType: anonymizer.EntityEmail, value: "alice@example.com"},
		}),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(`{"messages":[{"role":"user","content":"Email alice@example.com"}]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Accept-Encoding", "gzip")

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read proxy response body: %v", err)
	}
	if upstreamAcceptEncoding != "gzip" {
		t.Fatalf("upstream accept-encoding = %q, want gzip from automatic transport decompression", upstreamAcceptEncoding)
	}
	if strings.Contains(upstreamBody, "alice@example.com") {
		t.Fatalf("upstream body = %q, want anonymized request", upstreamBody)
	}
	if got := string(body); !strings.Contains(got, "alice@example.com") {
		t.Fatalf("response body = %q, want restored email", got)
	}
}

func TestSessionPromptAnonymizerLogsOnlyStats(t *testing.T) {
	body := []byte(`{"model":"claude","messages":[{"role":"user","content":[{"type":"text","text":"before <session>Email: alice@example.com\nTel: 06 12 34 56 78</session> after","cache_control":{"type":"ephemeral"}}]}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	anonymizedBody := newTestPromptAnonymizer().anonymize(context.Background(), logger, string(body))
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

func TestSessionPromptAnonymizerPreservesTopLevelSystemPrompts(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"rules <session>Contact alice@example.com</session> keep alice@example.com"}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	anonymizedBody := newTestPromptAnonymizer().anonymize(context.Background(), logger, string(body))

	if anonymizedBody != string(body) {
		t.Fatalf("body = %q, want top-level system preserved", anonymizedBody)
	}
	if logs.String() != "" {
		t.Fatalf("logs = %q, want no anonymization logs", logs.String())
	}
}

func TestSessionPromptAnonymizerPreservesSystemReminderAndAnonymizesUserContent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>Contact alice@example.com</system-reminder>"},{"type":"text","text":"Donne moi l'IBAN FR76 3000 6000 0112 3456 7890 189"}]}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	anonymizedBody := newTestPromptAnonymizer().anonymize(context.Background(), logger, string(body))
	logOutput := logs.String()

	if strings.Contains(anonymizedBody, "FR76 3000 6000 0112 3456 7890 189") {
		t.Fatalf("body = %q, want user content IBAN anonymized", anonymizedBody)
	}
	if !strings.Contains(anonymizedBody, "Donne moi l'IBAN [IBAN_1]") {
		t.Fatalf("body = %q, want anonymized user content", anonymizedBody)
	}
	if !strings.Contains(anonymizedBody, "<system-reminder>Contact alice@example.com</system-reminder>") {
		t.Fatalf("body = %q, want system reminder preserved", anonymizedBody)
	}
	if strings.Contains(anonymizedBody, "[EMAIL_1]") {
		t.Fatalf("body = %q, did not want system reminder email anonymized", anonymizedBody)
	}
	if !strings.Contains(logOutput, `"IBAN":1`) {
		t.Fatalf("logs = %q, want IBAN stats", logOutput)
	}
	if strings.Contains(logOutput, `"EMAIL"`) {
		t.Fatalf("logs = %q, did not want system reminder email stats", logOutput)
	}
	for _, unexpected := range []string{"alice@example.com", "FR76 3000 6000 0112 3456 7890 189", "[EMAIL_1]", "[IBAN_1]", "prompt_original", "prompt_anonymized"} {
		if strings.Contains(logOutput, unexpected) {
			t.Fatalf("logs = %q, did not want %q", logOutput, unexpected)
		}
	}
}

func TestSessionPromptAnonymizerPreservesSystemReminderInsideMixedString(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"Email bob@example.com <system-reminder>Contact alice@example.com</system-reminder> Phone 06 12 34 56 78"}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	anonymizedBody := newTestPromptAnonymizer().anonymize(context.Background(), logger, string(body))
	logOutput := logs.String()

	if strings.Contains(anonymizedBody, "bob@example.com") || strings.Contains(anonymizedBody, "06 12 34 56 78") {
		t.Fatalf("body = %q, want user content values anonymized", anonymizedBody)
	}
	if !strings.Contains(anonymizedBody, `"content":"Email [EMAIL_1] <system-reminder>Contact alice@example.com</system-reminder> Phone [PHONE_1]"`) {
		t.Fatalf("body = %q, want only system reminder preserved", anonymizedBody)
	}
	if !strings.Contains(logOutput, `"EMAIL":1`) || !strings.Contains(logOutput, `"PHONE":1`) {
		t.Fatalf("logs = %q, want user content stats only", logOutput)
	}
	for _, unexpected := range []string{"alice@example.com", "bob@example.com", "06 12 34 56 78", "[EMAIL_1]", "[PHONE_1]", "prompt_original", "prompt_anonymized"} {
		if strings.Contains(logOutput, unexpected) {
			t.Fatalf("logs = %q, did not want %q", logOutput, unexpected)
		}
	}
}

func TestSessionPromptAnonymizerLogsMultipleSessionsWithStableTokens(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<session>Email: alice@example.com</session> mid <session>Email: bob@example.com and alice@example.com</session>"}]}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	anonymizedBody := newTestPromptAnonymizer().anonymize(context.Background(), logger, string(body))
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

func TestSessionPromptAnonymizerAnonymizesNonUserTextContext(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"alice@example.com outside session"}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	anonymizedBody := newTestPromptAnonymizer().anonymize(context.Background(), logger, string(body))

	if logs.String() != "" {
		t.Fatalf("logs = %q, want no anonymization logs", logs.String())
	}
	if anonymizedBody != string(body) {
		t.Fatalf("body = %q, want top-level system text preserved", anonymizedBody)
	}
}

func TestSessionPromptAnonymizerAnonymizesDocumentTextSource(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","media_type":"text/plain","data":"Owner alice@example.com uses gitleaks-secret"}}]}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	promptAnonymizer := newTestPromptAnonymizerWithEngine(anonymizer.NewService([]anonymizer.Detector{
		literalDetector{entityType: anonymizer.EntityEmail, value: "alice@example.com"},
		literalDetector{entityType: anonymizer.EntitySecret, value: "gitleaks-secret"},
	}))
	anonymizedBody := promptAnonymizer.anonymize(context.Background(), logger, string(body))
	logOutput := logs.String()

	if strings.Contains(anonymizedBody, "alice@example.com") || strings.Contains(anonymizedBody, "gitleaks-secret") {
		t.Fatalf("body = %q, want document text source anonymized", anonymizedBody)
	}
	if !strings.Contains(anonymizedBody, `"data":"Owner [EMAIL_1] uses [SECRET_1]"`) {
		t.Fatalf("body = %q, want anonymized document data", anonymizedBody)
	}
	if !strings.Contains(anonymizedBody, `"type":"document"`) || !strings.Contains(anonymizedBody, `"type":"text"`) || !strings.Contains(anonymizedBody, `"media_type":"text/plain"`) {
		t.Fatalf("body = %q, want document metadata preserved", anonymizedBody)
	}
	if !strings.Contains(logOutput, `"EMAIL":1`) || !strings.Contains(logOutput, `"SECRET":1`) {
		t.Fatalf("logs = %q, want document anonymization stats", logOutput)
	}
}

func TestSessionPromptAnonymizerAnonymizesToolResults(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_123","name":"Read","input":{"file_path":"/tmp/a.txt"}},{"type":"tool_use","id":"toolu_456","name":"Grep","input":{"pattern":"IBAN"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":"Email alice@example.com"},{"type":"tool_result","tool_use_id":"toolu_456","content":[{"type":"text","text":"IBAN FR76 3000 6000 0112 3456 7890 189"}]}]}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	anonymizedBody := newTestPromptAnonymizer().anonymize(context.Background(), logger, string(body))
	logOutput := logs.String()

	if strings.Contains(anonymizedBody, "alice@example.com") || strings.Contains(anonymizedBody, "FR76 3000 6000 0112 3456 7890 189") {
		t.Fatalf("body = %q, want tool results anonymized", anonymizedBody)
	}
	if !strings.Contains(anonymizedBody, `"content":"Email [EMAIL_1]"`) || !strings.Contains(anonymizedBody, `"text":"IBAN [IBAN_1]"`) {
		t.Fatalf("body = %q, want string and block tool results anonymized", anonymizedBody)
	}
	if !strings.Contains(anonymizedBody, `"tool_use_id":"toolu_123"`) || !strings.Contains(anonymizedBody, `"tool_use_id":"toolu_456"`) {
		t.Fatalf("body = %q, want tool result ids preserved", anonymizedBody)
	}
	if !strings.Contains(logOutput, `"EMAIL":1`) || !strings.Contains(logOutput, `"IBAN":1`) {
		t.Fatalf("logs = %q, want tool result stats", logOutput)
	}
}

func TestSessionPromptAnonymizerPreservesNonReadToolResults(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_123","name":"Bash","input":{"command":"date"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":"Email alice@example.com"}]}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	anonymizedBody := newTestPromptAnonymizer().anonymize(context.Background(), logger, string(body))

	if anonymizedBody != string(body) {
		t.Fatalf("body = %q, want non-read tool result preserved", anonymizedBody)
	}
	if logs.String() != "" {
		t.Fatalf("logs = %q, want no anonymization logs", logs.String())
	}
}

func TestSessionPromptAnonymizerKeepsTokensStableAcrossRequestContexts(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_123","name":"Read","input":{"file_path":"/tmp/a.txt"}}]},{"role":"user","content":[{"type":"text","text":"Email alice@example.com"},{"type":"document","source":{"type":"text","data":"File owner alice@example.com"}},{"type":"tool_result","tool_use_id":"toolu_123","content":"Tool saw alice@example.com"}]}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	anonymizedBody := newTestPromptAnonymizer().anonymize(context.Background(), logger, string(body))

	if strings.Contains(anonymizedBody, "alice@example.com") {
		t.Fatalf("body = %q, want email anonymized everywhere", anonymizedBody)
	}
	if strings.Count(anonymizedBody, "[EMAIL_1]") != 3 {
		t.Fatalf("body = %q, want stable email token across prompt, file, and tool result", anonymizedBody)
	}
	if !strings.Contains(logs.String(), `"EMAIL":3`) {
		t.Fatalf("logs = %q, want aggregated email stats", logs.String())
	}
}

func TestSessionPromptAnonymizerPreservesMetadataAndBase64Sources(t *testing.T) {
	body := []byte(`{"model":"claude","id":"msg_alice@example.com","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_alice@example.com","name":"Read","input":{"file_path":"/tmp/a.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_alice@example.com","name":"lookup_alice@example.com","content":"Email alice@example.com"},{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"YWxpY2VAZXhhbXBsZS5jb20="}}]}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	anonymizedBody := newTestPromptAnonymizer().anonymize(context.Background(), logger, string(body))

	for _, preserved := range []string{
		`"model":"claude"`,
		`"id":"msg_alice@example.com"`,
		`"role":"user"`,
		`"type":"tool_result"`,
		`"tool_use_id":"toolu_alice@example.com"`,
		`"name":"lookup_alice@example.com"`,
		`"type":"base64"`,
		`"media_type":"application/pdf"`,
		`"data":"YWxpY2VAZXhhbXBsZS5jb20="`,
	} {
		if !strings.Contains(anonymizedBody, preserved) {
			t.Fatalf("body = %q, want preserved metadata %s", anonymizedBody, preserved)
		}
	}
	if !strings.Contains(anonymizedBody, `"content":"Email [EMAIL_1]"`) {
		t.Fatalf("body = %q, want tool result content anonymized", anonymizedBody)
	}
	if !strings.Contains(logs.String(), `"EMAIL":1`) {
		t.Fatalf("logs = %q, want only text content email stats", logs.String())
	}
}

func TestSessionPromptAnonymizerDoesNotKeepTokensAcrossCalls(t *testing.T) {
	promptAnonymizer := newTestPromptAnonymizer()
	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)

	promptAnonymizer.anonymize(context.Background(), logger, `{"messages":[{"role":"user","content":[{"type":"text","text":"<session>Email: alice@example.com</session>"}]}]}`)
	second := promptAnonymizer.anonymize(context.Background(), logger, `{"messages":[{"role":"user","content":[{"type":"text","text":"<session>Email: bob@example.com</session>"}]}]}`)

	if !strings.Contains(second, `<session>Email: [EMAIL_1]</session>`) {
		t.Fatalf("body = %q, want token state isolated across calls", second)
	}
	if !strings.Contains(logs.String(), `"EMAIL":1`) {
		t.Fatalf("logs = %q, want anonymized stats", logs.String())
	}
	for _, unexpected := range []string{"alice@example.com", "bob@example.com", "[EMAIL_1]", "prompt_original", "prompt_anonymized"} {
		if strings.Contains(logs.String(), unexpected) {
			t.Fatalf("logs = %q, did not want %q", logs.String(), unexpected)
		}
	}
}

func TestProxyForwardsAnonymizedSessionPrompts(t *testing.T) {
	var upstreamBody string
	responseBody := `{"content":"visible response"}`

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamBody = string(body)

		writer.Header().Set("x-upstream", "ok")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(responseBody))
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.DebugLevel)
	handler, err := NewProxyHandler(Config{
		Target:     target,
		Logger:     &logger,
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(`{"messages":[{"role":"user","content":[{"type":"text","text":"before <session>Email: alice@example.com</session> after"},{"type":"text","text":"IBAN FR76 3000 6000 0112 3456 7890 189"}]}]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()
	proxyResponseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read proxy response body: %v", err)
	}
	if got := string(proxyResponseBody); got != responseBody {
		t.Fatalf("response body = %q, want %q", got, responseBody)
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

	logOutput := logs.String()
	if !strings.Contains(logOutput, `"stage":"before_anonymization"`) {
		t.Fatalf("logs = %q, want pre-anonymization request body", logOutput)
	}
	if !strings.Contains(logOutput, "alice@example.com") || !strings.Contains(logOutput, "FR76 3000 6000 0112 3456 7890 189") {
		t.Fatalf("logs = %q, want original request values before anonymization", logOutput)
	}
	if !strings.Contains(logOutput, `"stage":"after_anonymization"`) {
		t.Fatalf("logs = %q, want post-anonymization request body", logOutput)
	}
	if !strings.Contains(logOutput, `<session>Email: [EMAIL_1]</session>`) || !strings.Contains(logOutput, `IBAN [IBAN_1]`) {
		t.Fatalf("logs = %q, want anonymized request body after anonymization", logOutput)
	}
	if strings.Contains(logOutput, `"direction":"response"`) || strings.Contains(logOutput, "visible response") {
		t.Fatalf("logs = %q, want no response traffic log", logOutput)
	}
	if !strings.Contains(logOutput, `"EMAIL":1`) || !strings.Contains(logOutput, `"IBAN":1`) {
		t.Fatalf("logs = %q, want anonymized stats", logOutput)
	}
	for _, unexpected := range []string{"request.", "response.", "upstream.", "prompt_original", "prompt_anonymized"} {
		if strings.Contains(logOutput, unexpected) {
			t.Fatalf("logs = %q, did not want %q", logOutput, unexpected)
		}
	}
}

func TestProxyUsesInjectedExternalDetectors(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamBody = string(body)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, upstream.URL),
		Logger: &logger,
		Anonymizer: anonymizer.NewService([]anonymizer.Detector{
			literalDetector{entityType: anonymizer.EntitySecret, value: "gitleaks-secret"},
		}),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"token gitleaks-secret"}]}`))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	if strings.Contains(upstreamBody, "gitleaks-secret") {
		t.Fatalf("upstream body = %q, want external detector value anonymized", upstreamBody)
	}
	if !strings.Contains(upstreamBody, "token [SECRET_1]") {
		t.Fatalf("upstream body = %q, want secret token", upstreamBody)
	}
	if !strings.Contains(logs.String(), `"SECRET":1`) {
		t.Fatalf("logs = %q, want secret stats", logs.String())
	}
}

func TestProxyAnonymizesOpenAIResponsesRequests(t *testing.T) {
	var upstreamPath string
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamPath = request.URL.Path
		upstreamBody = string(body)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, upstream.URL),
		},
		Logger:     &logger,
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	body := `{"model":"gpt-5.4","instructions":"Email alice@example.com","input":[{"role":"user","content":[{"type":"input_text","text":"Phone 06 12 34 56 78"}]},{"type":"function_call_output","output":"IBAN FR76 3000 6000 0112 3456 7890 189"}]}`
	response, err := server.Client().Post(server.URL+"/v1/responses", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("response status = %d, want 200", response.StatusCode)
	}
	if upstreamPath != "/v1/responses" {
		t.Fatalf("upstream path = %q, want /v1/responses", upstreamPath)
	}
	for _, value := range []string{"alice@example.com", "06 12 34 56 78", "FR76 3000 6000 0112 3456 7890 189"} {
		if strings.Contains(upstreamBody, value) {
			t.Fatalf("upstream body = %q, did not want %q", upstreamBody, value)
		}
	}
	for _, token := range []string{"[EMAIL_1]", "[PHONE_1]", "[IBAN_1]"} {
		if !strings.Contains(upstreamBody, token) {
			t.Fatalf("upstream body = %q, want token %s", upstreamBody, token)
		}
	}
	if strings.Contains(logs.String(), "pii_findings") || strings.Contains(logs.String(), "[EMAIL_3]") || strings.Contains(logs.String(), "[PHONE_2]") {
		t.Fatalf("logs = %q, want no pii findings by default", logs.String())
	}
}

func TestProxyRestoresOpenAIResponsePlaceholdersAcrossRuns(t *testing.T) {
	var upstreamBodies []string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamBodies = append(upstreamBodies, string(body))

		writer.Header().Set("content-type", "application/json")
		writer.WriteHeader(http.StatusOK)
		switch len(upstreamBodies) {
		case 1:
			_, _ = writer.Write([]byte(`{"id":"resp_1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Reply to [EMAIL_1]"}]}]}`))
		case 2:
			_, _ = writer.Write([]byte(`{"id":"resp_2","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Reply to [EMAIL_1] and [PHONE_1]"}]}]}`))
		default:
			t.Fatalf("unexpected upstream call %d", len(upstreamBodies))
		}
	}))
	defer upstream.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, upstream.URL),
		},
		Logger:     ptrLogger(zerolog.Nop()),
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	firstResponse, err := server.Client().Post(
		server.URL+"/v1/responses",
		"application/json",
		strings.NewReader(`{"model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_text","text":"Email alice@example.com"}]}]}`),
	)
	if err != nil {
		t.Fatalf("call first proxy request: %v", err)
	}
	defer firstResponse.Body.Close()

	firstBody, err := io.ReadAll(firstResponse.Body)
	if err != nil {
		t.Fatalf("read first proxy response: %v", err)
	}
	if strings.Contains(string(firstBody), "[EMAIL_1]") || !strings.Contains(string(firstBody), "alice@example.com") {
		t.Fatalf("first response body = %q, want restored email placeholder", firstBody)
	}

	secondResponse, err := server.Client().Post(
		server.URL+"/v1/responses",
		"application/json",
		strings.NewReader(`{"model":"gpt-5.4","previous_response_id":"resp_1","input":[{"role":"user","content":[{"type":"input_text","text":"Phone 06 12 34 56 78"}]}]}`),
	)
	if err != nil {
		t.Fatalf("call second proxy request: %v", err)
	}
	defer secondResponse.Body.Close()

	secondBody, err := io.ReadAll(secondResponse.Body)
	if err != nil {
		t.Fatalf("read second proxy response: %v", err)
	}
	if strings.Contains(string(secondBody), "[EMAIL_1]") || strings.Contains(string(secondBody), "[PHONE_1]") {
		t.Fatalf("second response body = %q, want restored placeholders", secondBody)
	}
	if !strings.Contains(string(secondBody), "alice@example.com") || !strings.Contains(string(secondBody), "06 12 34 56 78") {
		t.Fatalf("second response body = %q, want restored prior and current values", secondBody)
	}

	if len(upstreamBodies) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(upstreamBodies))
	}
	if strings.Contains(upstreamBodies[0], "alice@example.com") || !strings.Contains(upstreamBodies[0], "[EMAIL_1]") {
		t.Fatalf("first upstream body = %q, want anonymized email", upstreamBodies[0])
	}
	if !strings.Contains(upstreamBodies[1], `"previous_response_id":"resp_1"`) {
		t.Fatalf("second upstream body = %q, want previous response id", upstreamBodies[1])
	}
	if strings.Contains(upstreamBodies[1], "06 12 34 56 78") || !strings.Contains(upstreamBodies[1], "[PHONE_1]") {
		t.Fatalf("second upstream body = %q, want anonymized phone", upstreamBodies[1])
	}
}

func TestProxyLogsOpenAIPIIFindingsWhenEnabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, upstream.URL),
		},
		Logger: &logger,
		Anonymizer: anonymizer.NewService([]anonymizer.Detector{
			literalDetector{entityType: anonymizer.EntityEmail, value: "email-value-1"},
			literalDetector{entityType: anonymizer.EntityPhone, value: "phone-value-1"},
		}),
		LogPIIFindings: true,
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	body := `{"model":"gpt-5.4","instructions":"Email email-value-1","input":[{"role":"user","content":[{"type":"input_text","text":"Phone phone-value-1"}]}]}`
	response, err := server.Client().Post(server.URL+"/v1/responses", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	logOutput := logs.String()
	if !strings.Contains(logOutput, `"message":"request body anonymized"`) {
		t.Fatalf("logs = %q, want anonymized log", logOutput)
	}
	if !strings.Contains(logOutput, `"pii_findings":[`) {
		t.Fatalf("logs = %q, want pii findings", logOutput)
	}
	if !strings.Contains(logOutput, `"type":"EMAIL"`) || !strings.Contains(logOutput, `"value":"email-value-1"`) || !strings.Contains(logOutput, `"token":"[EMAIL_1]"`) {
		t.Fatalf("logs = %q, want email finding with original value and token", logOutput)
	}
	if !strings.Contains(logOutput, `"type":"PHONE"`) || !strings.Contains(logOutput, `"value":"phone-value-1"`) || !strings.Contains(logOutput, `"token":"[PHONE_1]"`) {
		t.Fatalf("logs = %q, want phone finding with original value and token", logOutput)
	}
	if strings.Contains(logOutput, `"body":`) || strings.Contains(logOutput, `"stage":"before_anonymization"`) || strings.Contains(logOutput, `"stage":"after_anonymization"`) {
		t.Fatalf("logs = %q, want findings without full traffic bodies", logOutput)
	}
}

func TestProxyDeanonymizesOpenAIResponses(t *testing.T) {
	var upstreamBody string

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamBody = string(body)
		token := extractToken(t, upstreamBody, `Email ([^"]+)`)
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(fmt.Sprintf(`{"output":[{"type":"message","content":[{"type":"output_text","text":"Echo %s"}]}]}`, token)))
	}))
	defer upstream.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, upstream.URL),
		},
		Logger: ptrLogger(zerolog.Nop()),
		Anonymizer: anonymizer.NewService([]anonymizer.Detector{
			literalDetector{entityType: anonymizer.EntityEmail, value: "alice@example.com"},
		}),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"input":"Email alice@example.com"}`))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read proxy response body: %v", err)
	}
	if strings.Contains(upstreamBody, "alice@example.com") {
		t.Fatalf("upstream body = %q, want anonymized request", upstreamBody)
	}
	if got := string(body); !strings.Contains(got, "alice@example.com") {
		t.Fatalf("response body = %q, want restored email", got)
	}
}

func TestProxyAnonymizesOpenAIChatCompletionsRequests(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamBody = string(body)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, upstream.URL),
		},
		Logger:     ptrLogger(zerolog.Nop()),
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	body := `{"model":"gpt-5.4","messages":[{"role":"user","content":[{"type":"text","text":"Contact alice@example.com"}]},{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"lookup","arguments":"{\"email\":\"bob@example.com\"}"}}]},{"role":"tool","content":"Phone 06 12 34 56 78"}]}`
	response, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	for _, value := range []string{"alice@example.com", "bob@example.com", "06 12 34 56 78"} {
		if strings.Contains(upstreamBody, value) {
			t.Fatalf("upstream body = %q, did not want %q", upstreamBody, value)
		}
	}
	if strings.Count(upstreamBody, "[EMAIL_") != 2 || !strings.Contains(upstreamBody, "[PHONE_1]") {
		t.Fatalf("upstream body = %q, want anonymized chat content and tool calls", upstreamBody)
	}
}

func TestProxyFailClosedForInvalidOpenAIJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Fatal("upstream should not be called")
	}))
	defer upstream.Close()

	statsRecorder := &fakeStatsRecorder{}
	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, upstream.URL),
		},
		Logger:        ptrLogger(zerolog.Nop()),
		Anonymizer:    newTestAnonymizer(),
		StatsRecorder: statsRecorder,
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"input":`))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("response status = %d, want 400", response.StatusCode)
	}
	if !statsRecorder.hasEvent(statlog.EventRequestBodyError) {
		t.Fatalf("stats events = %#v, want request body error", statsRecorder.events)
	}
}

func TestProxyRoutesCodexResponsesByChatGPTAuth(t *testing.T) {
	var apiCalls int
	var chatGPTPath string
	plainAPI := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		apiCalls++
		writer.WriteHeader(http.StatusOK)
	}))
	defer plainAPI.Close()
	chatGPT := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		chatGPTPath = request.URL.Path
		writer.WriteHeader(http.StatusOK)
	}))
	defer chatGPT.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, plainAPI.URL),
		},
		ChatGPTTarget: mustParseURL(t, chatGPT.URL),
		Logger:        ptrLogger(zerolog.Nop()),
		Anonymizer:    newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/backend-api/codex/responses", strings.NewReader(`{"input":"Email alice@example.com"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("ChatGPT-Account-ID", "acct_123")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	response.Body.Close()

	if apiCalls != 0 {
		t.Fatalf("api calls = %d, want 0", apiCalls)
	}
	if chatGPTPath != "/backend-api/codex/responses" {
		t.Fatalf("chatgpt path = %q, want /backend-api/codex/responses", chatGPTPath)
	}
}

func TestProxyRoutesCodexResponsesByChatGPTJWT(t *testing.T) {
	var chatGPTAccountID string
	chatGPT := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		chatGPTAccountID = request.Header.Get("ChatGPT-Account-ID")
		writer.WriteHeader(http.StatusOK)
	}))
	defer chatGPT.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, "http://api.openai.invalid"),
		},
		ChatGPTTarget: mustParseURL(t, chatGPT.URL),
		Logger:        ptrLogger(zerolog.Nop()),
		Anonymizer:    newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct_from_jwt"},
	}))
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	response.Body.Close()

	if chatGPTAccountID != "acct_from_jwt" {
		t.Fatalf("ChatGPT-Account-ID = %q, want acct_from_jwt", chatGPTAccountID)
	}
}

func TestProxyReturnsSyntheticCodexModelsForChatGPTAuth(t *testing.T) {
	chatGPT := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "nope", http.StatusForbidden)
	}))
	defer chatGPT.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, "http://api.openai.invalid"),
		},
		ChatGPTTarget: mustParseURL(t, chatGPT.URL),
		Logger:        ptrLogger(zerolog.Nop()),
		Anonymizer:    newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/models", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("chatgpt-account-id", "acct_123")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	if response.StatusCode != http.StatusOK {
		t.Fatalf("response status = %d, want 200", response.StatusCode)
	}
	if !strings.Contains(string(body), `"object":"list"`) || !strings.Contains(string(body), `"id":"gpt-5.4"`) {
		t.Fatalf("response body = %s, want synthetic codex model list", body)
	}
}

func TestProxyRelaysAndAnonymizesCodexResponsesWebSocket(t *testing.T) {
	var upstreamPath string
	var upstreamAuthorization string
	var upstreamBeta string
	var upstreamProtocol string
	var upstreamFrame string
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamPath = request.URL.Path
		upstreamAuthorization = request.Header.Get("Authorization")
		upstreamBeta = request.Header.Get("OpenAI-Beta")
		upstreamProtocol = request.Header.Get("Sec-WebSocket-Protocol")
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			t.Fatalf("upstream upgrade: %v", err)
		}
		defer conn.Close()
		_, message, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("upstream read: %v", err)
		}
		upstreamFrame = string(message)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed"}`)); err != nil {
			t.Fatalf("upstream write: %v", err)
		}
	}))
	defer upstream.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, upstream.URL),
		},
		Logger:     ptrLogger(zerolog.Nop()),
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	header := http.Header{}
	header.Set("Authorization", "Bearer sk-test")
	header.Set("OpenAI-Beta", "foo=bar")
	header.Set("Sec-WebSocket-Protocol", "realtime")
	conn, _, err := websocket.DefaultDialer.Dial(httpToWS(server.URL)+"/v1/responses", header)
	if err != nil {
		t.Fatalf("dial proxy websocket: %v", err)
	}
	defer conn.Close()

	frame := `{"type":"response.create","response":{"instructions":"Email alice@example.com","input":[{"role":"user","content":[{"type":"input_text","text":"Phone 06 12 34 56 78"}]}]}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write proxy websocket: %v", err)
	}
	_, responseMessage, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read proxy websocket: %v", err)
	}

	if string(responseMessage) != `{"type":"response.completed"}` {
		t.Fatalf("proxy websocket response = %s", responseMessage)
	}
	if upstreamPath != "/v1/responses" {
		t.Fatalf("upstream path = %q, want /v1/responses", upstreamPath)
	}
	if upstreamAuthorization != "Bearer sk-test" {
		t.Fatalf("upstream authorization = %q, want bearer", upstreamAuthorization)
	}
	if upstreamProtocol != "realtime" {
		t.Fatalf("upstream protocol = %q, want realtime", upstreamProtocol)
	}
	if !strings.Contains(upstreamBeta, "foo=bar") || !strings.Contains(upstreamBeta, "responses_websockets=2026-02-06") {
		t.Fatalf("upstream OpenAI-Beta = %q, want merged beta", upstreamBeta)
	}
	for _, value := range []string{"alice@example.com", "06 12 34 56 78"} {
		if strings.Contains(upstreamFrame, value) {
			t.Fatalf("upstream frame = %q, did not want %q", upstreamFrame, value)
		}
	}
	if !strings.Contains(upstreamFrame, "[EMAIL_1]") || !strings.Contains(upstreamFrame, "[PHONE_1]") {
		t.Fatalf("upstream frame = %q, want anonymized values", upstreamFrame)
	}
}

func TestProxyRelaysAndDeanonymizesCodexResponsesWebSocket(t *testing.T) {
	var upstreamFrame string
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			t.Fatalf("upstream upgrade: %v", err)
		}
		defer conn.Close()

		_, message, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("upstream read: %v", err)
		}
		upstreamFrame = string(message)
		token := extractToken(t, upstreamFrame, `Email ([^"]+)`)
		reply := fmt.Sprintf(`{"response":{"output":[{"type":"message","content":[{"type":"output_text","text":"Back %s"}]}]}}`, token)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(reply)); err != nil {
			t.Fatalf("upstream write: %v", err)
		}
	}))
	defer upstream.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, upstream.URL),
		},
		Logger: ptrLogger(zerolog.Nop()),
		Anonymizer: anonymizer.NewService([]anonymizer.Detector{
			literalDetector{entityType: anonymizer.EntityEmail, value: "alice@example.com"},
		}),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	header := http.Header{}
	header.Set("Authorization", "Bearer sk-test")
	header.Set("Sec-WebSocket-Protocol", "realtime")
	conn, _, err := websocket.DefaultDialer.Dial(httpToWS(server.URL)+"/v1/responses", header)
	if err != nil {
		t.Fatalf("dial proxy websocket: %v", err)
	}
	defer conn.Close()

	frame := `{"type":"response.create","response":{"instructions":"Email alice@example.com"}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write proxy websocket: %v", err)
	}
	_, responseMessage, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read proxy websocket: %v", err)
	}

	if strings.Contains(upstreamFrame, "alice@example.com") {
		t.Fatalf("upstream frame = %q, want anonymized request", upstreamFrame)
	}
	if got := string(responseMessage); !strings.Contains(got, "alice@example.com") {
		t.Fatalf("proxy websocket response = %q, want restored email", got)
	}
}

func TestProxyRestoresCodexResponsesWebSocketPlaceholders(t *testing.T) {
	var upstreamFrame string
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			t.Fatalf("upstream upgrade: %v", err)
		}
		defer conn.Close()

		_, message, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("upstream read: %v", err)
		}
		upstreamFrame = string(message)

		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.output_text.delta","delta":"Email [EMAIL_1]"}`)); err != nil {
			t.Fatalf("upstream write delta: %v", err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_ws_1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Final [EMAIL_1]"}]}]}}`)); err != nil {
			t.Fatalf("upstream write completed: %v", err)
		}
	}))
	defer upstream.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, upstream.URL),
		},
		Logger:     ptrLogger(zerolog.Nop()),
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(httpToWS(server.URL)+"/v1/responses", http.Header{})
	if err != nil {
		t.Fatalf("dial proxy websocket: %v", err)
	}
	defer conn.Close()

	frame := `{"type":"response.create","response":{"input":[{"role":"user","content":[{"type":"input_text","text":"Email alice@example.com"}]}]}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write proxy websocket: %v", err)
	}

	_, deltaMessage, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read proxy websocket delta: %v", err)
	}
	if strings.Contains(string(deltaMessage), "[EMAIL_1]") || !strings.Contains(string(deltaMessage), "alice@example.com") {
		t.Fatalf("proxy websocket delta = %q, want restored email", deltaMessage)
	}

	_, completedMessage, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read proxy websocket completed: %v", err)
	}
	if strings.Contains(string(completedMessage), "[EMAIL_1]") || !strings.Contains(string(completedMessage), "alice@example.com") {
		t.Fatalf("proxy websocket completed = %q, want restored email", completedMessage)
	}

	if strings.Contains(upstreamFrame, "alice@example.com") || !strings.Contains(upstreamFrame, "[EMAIL_1]") {
		t.Fatalf("upstream frame = %q, want anonymized email", upstreamFrame)
	}
}

func TestProxyRestoresCodexResponsesWebSocketPlaceholdersAcrossFrames(t *testing.T) {
	var upstreamFrames []string
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			t.Fatalf("upstream upgrade: %v", err)
		}
		defer conn.Close()

		_, firstMessage, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("upstream first read: %v", err)
		}
		upstreamFrames = append(upstreamFrames, string(firstMessage))
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_ws_1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"First [EMAIL_1]"}]}]}}`)); err != nil {
			t.Fatalf("upstream first write: %v", err)
		}

		_, secondMessage, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("upstream second read: %v", err)
		}
		upstreamFrames = append(upstreamFrames, string(secondMessage))
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_ws_2","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Second [EMAIL_1] [PHONE_1]"}]}]}}`)); err != nil {
			t.Fatalf("upstream second write: %v", err)
		}
	}))
	defer upstream.Close()

	handler, err := NewProxyHandler(Config{
		Target: mustParseURL(t, "http://anthropic.example"),
		RouteTargets: map[string]*url.URL{
			OpenAIRoutePrefix: mustParseURL(t, upstream.URL),
		},
		Logger:     ptrLogger(zerolog.Nop()),
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(httpToWS(server.URL)+"/v1/responses", http.Header{})
	if err != nil {
		t.Fatalf("dial proxy websocket: %v", err)
	}
	defer conn.Close()

	firstFrame := `{"type":"response.create","response":{"input":[{"role":"user","content":[{"type":"input_text","text":"Email alice@example.com"}]}]}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(firstFrame)); err != nil {
		t.Fatalf("write first proxy websocket frame: %v", err)
	}
	_, firstResponse, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read first proxy websocket response: %v", err)
	}
	if strings.Contains(string(firstResponse), "[EMAIL_1]") || !strings.Contains(string(firstResponse), "alice@example.com") {
		t.Fatalf("first proxy websocket response = %q, want restored email", firstResponse)
	}

	secondFrame := `{"type":"response.create","previous_response_id":"resp_ws_1","response":{"input":[{"role":"user","content":[{"type":"input_text","text":"Phone 06 12 34 56 78"}]}]}}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(secondFrame)); err != nil {
		t.Fatalf("write second proxy websocket frame: %v", err)
	}
	_, secondResponse, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read second proxy websocket response: %v", err)
	}
	if strings.Contains(string(secondResponse), "[EMAIL_1]") || strings.Contains(string(secondResponse), "[PHONE_1]") {
		t.Fatalf("second proxy websocket response = %q, want restored placeholders", secondResponse)
	}
	if !strings.Contains(string(secondResponse), "alice@example.com") || !strings.Contains(string(secondResponse), "06 12 34 56 78") {
		t.Fatalf("second proxy websocket response = %q, want restored email and phone", secondResponse)
	}

	if len(upstreamFrames) != 2 {
		t.Fatalf("upstream frames = %d, want 2", len(upstreamFrames))
	}
	if strings.Contains(upstreamFrames[0], "alice@example.com") || !strings.Contains(upstreamFrames[0], "[EMAIL_1]") {
		t.Fatalf("first upstream frame = %q, want anonymized email", upstreamFrames[0])
	}
	if strings.Contains(upstreamFrames[1], "06 12 34 56 78") || !strings.Contains(upstreamFrames[1], "[PHONE_1]") {
		t.Fatalf("second upstream frame = %q, want anonymized phone", upstreamFrames[1])
	}
}

func TestProxyAnonymizesMessagesCountTokensRequests(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamBody = string(body)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	handler, err := NewProxyHandler(Config{
		Target:     mustParseURL(t, upstream.URL),
		Logger:     &logger,
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/v1/messages/count_tokens", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"Email alice@example.com"}]}`))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	if strings.Contains(upstreamBody, "alice@example.com") {
		t.Fatalf("upstream body = %q, want count_tokens prompt anonymized", upstreamBody)
	}
	if !strings.Contains(upstreamBody, "Email [EMAIL_1]") {
		t.Fatalf("upstream body = %q, want email token", upstreamBody)
	}
	if !strings.Contains(logs.String(), `"EMAIL":1`) {
		t.Fatalf("logs = %q, want anonymized stats", logs.String())
	}
}

func TestProxyDoesNotAnonymizeNonMessagesRequests(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		upstreamBody = string(body)
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	handler, err := NewProxyHandler(Config{
		Target:     mustParseURL(t, upstream.URL),
		Logger:     &logger,
		Anonymizer: newTestAnonymizer(),
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	requestBody := `{"messages":[{"role":"user","content":"Email alice@example.com and Jean Dupont"}]}`
	response, err := server.Client().Post(server.URL+"/v1/models", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	if upstreamBody != requestBody {
		t.Fatalf("upstream body = %q, want original body %q", upstreamBody, requestBody)
	}
	if logs.String() != "" {
		t.Fatalf("logs = %q, want no anonymization logs", logs.String())
	}
}

// TestProxyRecordsRequestProcessedStats verifies that each processed request emits replacement counters for the dashboard.
func TestProxyRecordsRequestProcessedStats(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	statsRecorder := &fakeStatsRecorder{}
	handler, err := NewProxyHandler(Config{
		Target:        mustParseURL(t, upstream.URL),
		Logger:        ptrLogger(zerolog.Nop()),
		Anonymizer:    newTestAnonymizer(),
		StatsRecorder: statsRecorder,
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"Email alice@example.com"}]}`))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	processed := statsRecorder.lastEvent(statlog.EventRequestProcessed)
	if processed.Event == "" {
		t.Fatalf("stats events = %#v, want request processed event", statsRecorder.events)
	}
	if !processed.Anonymized || processed.TotalReplacements != 1 || processed.Counts["EMAIL"] != 1 {
		t.Fatalf("request processed event = %#v, want one EMAIL replacement", processed)
	}
}

// TestProxyRecordsProxyErrors verifies that upstream forwarding failures are counted separately from processed requests.
func TestProxyRecordsProxyErrors(t *testing.T) {
	statsRecorder := &fakeStatsRecorder{}
	handler, err := NewProxyHandler(Config{
		Target:        mustParseURL(t, "http://upstream.example"),
		Logger:        ptrLogger(zerolog.Nop()),
		Transport:     failingRoundTripper{},
		Anonymizer:    newTestAnonymizer(),
		StatsRecorder: statsRecorder,
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("response status = %d, want 502", response.StatusCode)
	}
	if !statsRecorder.hasEvent(statlog.EventProxyError) {
		t.Fatalf("stats events = %#v, want proxy error event", statsRecorder.events)
	}
	if !statsRecorder.hasEvent(statlog.EventRequestProcessed) {
		t.Fatalf("stats events = %#v, want request processed event", statsRecorder.events)
	}
}

// TestProxyRecordsRequestBodyErrors verifies that unreadable request bodies are counted without marking the request as processed.
func TestProxyRecordsRequestBodyErrors(t *testing.T) {
	statsRecorder := &fakeStatsRecorder{}
	handler, err := NewProxyHandler(Config{
		Target:        mustParseURL(t, "http://upstream.example"),
		Logger:        ptrLogger(zerolog.Nop()),
		Transport:     failingRoundTripper{},
		Anonymizer:    newTestAnonymizer(),
		StatsRecorder: statsRecorder,
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Body = errReadCloser{}
	response := httptest.NewRecorder()
	newTestRouter(handler).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("response status = %d, want 400", response.Code)
	}
	if !statsRecorder.hasEvent(statlog.EventRequestBodyError) {
		t.Fatalf("stats events = %#v, want request body error event", statsRecorder.events)
	}
	if statsRecorder.hasEvent(statlog.EventRequestProcessed) {
		t.Fatalf("stats events = %#v, did not want processed event after body read failure", statsRecorder.events)
	}
}

type testPromptAnonymizer struct {
	provider *providers.Anthropic
}

func newTestPromptAnonymizer() *testPromptAnonymizer {
	return newTestPromptAnonymizerWithEngine(newTestAnonymizer())
}

func newTestPromptAnonymizerWithEngine(engine providers.TextAnonymizer) *testPromptAnonymizer {
	provider, err := providers.NewAnthropic(providers.AnthropicConfig{
		RoutePrefix: AnthropicRoutePrefix,
		Anonymizer:  engine,
	})
	if err != nil {
		panic(err)
	}
	return &testPromptAnonymizer{provider: provider}
}

func (a *testPromptAnonymizer) anonymize(ctx context.Context, logger zerolog.Logger, body string) string {
	result := a.provider.Anonymize(ctx, logger, []byte(body))
	return string(result.Body)
}

func newTestRouter(handler gin.HandlerFunc) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Any("/*proxyPath", handler)
	return router
}

func newTestAnonymizer() *anonymizer.Service {
	return anonymizer.NewService(detectors.Default(true))
}

func testJWT(payload map[string]any) string {
	header := map[string]any{"alg": "none", "typ": "JWT"}
	return base64.RawURLEncoding.EncodeToString(mustMarshalJSON(header)) + "." +
		base64.RawURLEncoding.EncodeToString(mustMarshalJSON(payload)) + "."
}

func mustMarshalJSON(value any) []byte {
	content, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return content
}

func httpToWS(value string) string {
	return "ws" + strings.TrimPrefix(value, "http")
}

func extractToken(t *testing.T, input string, pattern string) string {
	t.Helper()

	matches := regexp.MustCompile(pattern).FindStringSubmatch(input)
	if len(matches) != 2 {
		t.Fatalf("input %q did not match token pattern %q", input, pattern)
	}
	return matches[1]
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return parsed
}

func decodeJSONValue(t *testing.T, label string, input string) any {
	t.Helper()

	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode %s: %v", label, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("decode %s: trailing data", label)
	}
	return value
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

type literalDetector struct {
	entityType anonymizer.EntityType
	value      string
}

func (d literalDetector) FindAll(text string) []anonymizer.Match {
	var matches []anonymizer.Match
	remaining := text
	offset := 0
	for {
		index := strings.Index(remaining, d.value)
		if index < 0 {
			return matches
		}
		start := offset + index
		end := start + len(d.value)
		matches = append(matches, anonymizer.Match{
			Start:      start,
			End:        end,
			Type:       d.entityType,
			Priority:   600,
			Normalized: d.value,
		})
		offset = end
		remaining = text[offset:]
	}
}

type fakeStatsRecorder struct {
	events []statlog.Event
	err    error
}

func (r *fakeStatsRecorder) Record(event statlog.Event) error {
	r.events = append(r.events, event)
	return r.err
}

func (r *fakeStatsRecorder) hasEvent(eventName string) bool {
	return r.lastEvent(eventName).Event != ""
}

func (r *fakeStatsRecorder) lastEvent(eventName string) statlog.Event {
	for index := len(r.events) - 1; index >= 0; index-- {
		if r.events[index].Event == eventName {
			return r.events[index]
		}
	}
	return statlog.Event{}
}

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("upstream unavailable")
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) {
	return 0, fmt.Errorf("read failed")
}

func (errReadCloser) Close() error {
	return nil
}

func ptrLogger(logger zerolog.Logger) *zerolog.Logger {
	return &logger
}
