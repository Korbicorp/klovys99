package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	"github.com/Korbicorp/klovis/internal/detectors"
	statlog "github.com/Korbicorp/klovis/internal/stats"
	"github.com/gin-gonic/gin"
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

func TestSessionPromptAnonymizerAnonymizesSystemPrompts(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"rules <session>Contact alice@example.com</session> keep alice@example.com"}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	anonymizedBody := newTestPromptAnonymizer().anonymize(context.Background(), logger, string(body))
	logOutput := logs.String()

	if !strings.Contains(anonymizedBody, `rules <session>Contact [EMAIL_1]</session> keep [EMAIL_1]`) {
		t.Fatalf("body = %q, want system content anonymized", anonymizedBody)
	}
	if !strings.Contains(logOutput, `"EMAIL":2`) {
		t.Fatalf("logs = %q, want anonymized stats", logOutput)
	}
	for _, unexpected := range []string{"alice@example.com", "[EMAIL_1]", "prompt_original", "prompt_anonymized"} {
		if strings.Contains(logOutput, unexpected) {
			t.Fatalf("logs = %q, did not want %q", logOutput, unexpected)
		}
	}
}

func TestSessionPromptAnonymizerAnonymizesUserContentOutsideSession(t *testing.T) {
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
	if !strings.Contains(anonymizedBody, "<system-reminder>Contact [EMAIL_1]</system-reminder>") {
		t.Fatalf("body = %q, want system reminder anonymized", anonymizedBody)
	}
	if !strings.Contains(logOutput, `"EMAIL":1`) || !strings.Contains(logOutput, `"IBAN":1`) {
		t.Fatalf("logs = %q, want IBAN stats", logOutput)
	}
	for _, unexpected := range []string{"alice@example.com", "FR76 3000 6000 0112 3456 7890 189", "[EMAIL_1]", "[IBAN_1]", "prompt_original", "prompt_anonymized"} {
		if strings.Contains(logOutput, unexpected) {
			t.Fatalf("logs = %q, did not want %q", logOutput, unexpected)
		}
	}
}

func TestSessionPromptAnonymizerLogsMultipleSessionsWithStableTokens(t *testing.T) {
	body := []byte(`{"text":"<session>Email: alice@example.com</session> mid <session>Email: bob@example.com and alice@example.com</session>"}`)

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

	if !strings.Contains(logs.String(), `"EMAIL":1`) {
		t.Fatalf("logs = %q, want email stats", logs.String())
	}
	if strings.Contains(anonymizedBody, "alice@example.com") || !strings.Contains(anonymizedBody, "[EMAIL_1] outside session") {
		t.Fatalf("body = %q, want system text anonymized", anonymizedBody)
	}
}

func TestSessionPromptAnonymizerAnonymizesDocumentTextSource(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","media_type":"text/plain","data":"Owner alice@example.com uses gitleaks-secret"}}]}]}`)

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	promptAnonymizer := newSessionPromptAnonymizer(anonymizer.NewService([]anonymizer.Detector{
		literalDetector{entityType: anonymizer.EntityEmail, value: "alice@example.com"},
		literalDetector{entityType: anonymizer.EntitySecret, value: "gitleaks-secret"},
	}), nil)
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
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":"Email alice@example.com"},{"type":"tool_result","tool_use_id":"toolu_456","content":[{"type":"text","text":"IBAN FR76 3000 6000 0112 3456 7890 189"}]}]}]}`)

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

func TestSessionPromptAnonymizerKeepsTokensStableAcrossRequestContexts(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"Email alice@example.com"},{"type":"document","source":{"type":"text","data":"File owner alice@example.com"}},{"type":"tool_result","tool_use_id":"toolu_123","content":"Tool saw alice@example.com"}]}]}`)

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
	body := []byte(`{"model":"claude","id":"msg_alice@example.com","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_alice@example.com","name":"lookup_alice@example.com","content":"Email alice@example.com"},{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"YWxpY2VAZXhhbXBsZS5jb20="}}]}]}`)

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

	promptAnonymizer.anonymize(context.Background(), logger, `{"text":"<session>Email: alice@example.com</session>"}`)
	second := promptAnonymizer.anonymize(context.Background(), logger, `{"text":"<session>Email: bob@example.com</session>"}`)

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
	if strings.Contains(logOutput, "alice@example.com") || strings.Contains(logOutput, "FR76 3000 6000 0112 3456 7890 189") {
		t.Fatalf("logs = %q, want final anonymized request only", logOutput)
	}
	if !strings.Contains(logOutput, `<session>Email: [EMAIL_1]</session>`) || !strings.Contains(logOutput, `IBAN [IBAN_1]`) {
		t.Fatalf("logs = %q, want final request body", logOutput)
	}
	if strings.Contains(logOutput, `"direction":"response"`) || strings.Contains(logOutput, "visible response") {
		t.Fatalf("logs = %q, want no response traffic log", logOutput)
	}
	if !strings.Contains(logOutput, `"EMAIL":1`) || !strings.Contains(logOutput, `"IBAN":1`) {
		t.Fatalf("logs = %q, want anonymized stats", logOutput)
	}
	for _, unexpected := range []string{"request.", "response.", "upstream.", "alice@example.com", "FR76 3000 6000 0112 3456 7890 189", "prompt_original", "prompt_anonymized"} {
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

func TestProxyUsesLLMMatches(t *testing.T) {
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
	matchFinder := &fakeMatchFinder{
		matches: []anonymizer.Match{
			{Start: 8, End: 19, Type: anonymizer.EntityPersonName, Priority: 50, Normalized: "jean dupont"},
		},
	}
	handler, err := NewProxyHandler(Config{
		Target:      mustParseURL(t, upstream.URL),
		Logger:      &logger,
		Anonymizer:  newTestAnonymizer(),
		MatchFinder: matchFinder,
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	server := httptest.NewServer(newTestRouter(handler))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/v1/messages", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"Bonjour Jean Dupont"}]}`))
	if err != nil {
		t.Fatalf("call proxy: %v", err)
	}
	defer response.Body.Close()

	if matchFinder.calls == 0 {
		t.Fatal("match finder was not called")
	}
	if strings.Contains(upstreamBody, "Jean Dupont") {
		t.Fatalf("upstream body = %q, want LLM entity anonymized", upstreamBody)
	}
	if !strings.Contains(upstreamBody, "Bonjour [PERSON_NAME_1]") {
		t.Fatalf("upstream body = %q, want person token", upstreamBody)
	}
	if !strings.Contains(logs.String(), `"PERSON_NAME":1`) {
		t.Fatalf("logs = %q, want LLM stats", logs.String())
	}
}

// TestProxyFallsBackWhenLLMRequestFails verifies that LLM failures are recorded while deterministic anonymization still runs.
func TestProxyFallsBackWhenLLMRequestFails(t *testing.T) {
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
	statsRecorder := &fakeStatsRecorder{}
	handler, err := NewProxyHandler(Config{
		Target:        mustParseURL(t, upstream.URL),
		Logger:        &logger,
		Anonymizer:    newTestAnonymizer(),
		MatchFinder:   &fakeMatchFinder{err: fmt.Errorf("llm down")},
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

	if response.StatusCode != http.StatusOK {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if strings.Contains(upstreamBody, "alice@example.com") {
		t.Fatalf("upstream body = %q, want regex fallback anonymization", upstreamBody)
	}
	if !strings.Contains(upstreamBody, "Email [EMAIL_1]") {
		t.Fatalf("upstream body = %q, want email token", upstreamBody)
	}

	logOutput := logs.String()
	if !strings.Contains(logOutput, "llm anonymization failed") || !strings.Contains(logOutput, `"EMAIL":1`) {
		t.Fatalf("logs = %q, want LLM error and regex stats", logOutput)
	}
	for _, unexpected := range []string{"alice@example.com"} {
		if strings.Contains(logOutput, unexpected) {
			t.Fatalf("logs = %q, did not want %q", logOutput, unexpected)
		}
	}
	if !statsRecorder.hasEvent(statlog.EventLLMError) {
		t.Fatalf("stats events = %#v, want LLM error event", statsRecorder.events)
	}
	processed := statsRecorder.lastEvent(statlog.EventRequestProcessed)
	if processed.Event == "" || !processed.Anonymized || processed.Counts["EMAIL"] != 1 {
		t.Fatalf("request processed stats = %#v, want anonymized EMAIL count", processed)
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

func newTestPromptAnonymizer() *sessionPromptAnonymizer {
	return newSessionPromptAnonymizer(newTestAnonymizer(), nil)
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

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return parsed
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

type fakeMatchFinder struct {
	matches []anonymizer.Match
	err     error
	calls   int
}

func (f *fakeMatchFinder) FindMatches(context.Context, string) ([]anonymizer.Match, error) {
	f.calls++
	return f.matches, f.err
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
