package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Korbicorp/klovis/internal/anonymizer"
)

type fakeExtractor struct {
	entities []Entity
	err      error
	calls    int
}

func (f *fakeExtractor) Extract(context.Context, string) ([]Entity, error) {
	f.calls++
	return f.entities, f.err
}

type fakeOllamaClient struct {
	model    string
	prompt   string
	format   any
	response string
	err      error
	calls    int
}

func (f *fakeOllamaClient) Generate(_ context.Context, model, prompt string, format any) (string, error) {
	f.calls++
	f.model = model
	f.prompt = prompt
	f.format = format
	return f.response, f.err
}

type receivedGenerateRequest struct {
	Model  string          `json:"model"`
	Prompt string          `json:"prompt"`
	Stream bool            `json:"stream"`
	Format json.RawMessage `json:"format"`
}

type testOllamaServer struct {
	t             *testing.T
	server        *httptest.Server
	response      string
	tagsCalled    bool
	generateCalls int
	receivedModel string
}

func newTestOllamaServer(t *testing.T, response string) *testOllamaServer {
	t.Helper()

	testServer := &testOllamaServer{
		t:        t,
		response: response,
	}
	testServer.server = httptest.NewServer(http.HandlerFunc(testServer.handle))
	return testServer
}

func (s *testOllamaServer) handle(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/api/tags":
		s.tagsCalled = true
		writer.WriteHeader(http.StatusOK)
	case "/api/generate":
		var received receivedGenerateRequest
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			s.t.Fatalf("decode generate request: %v", err)
		}
		s.generateCalls++
		s.receivedModel = received.Model
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(map[string]string{"response": s.response}); err != nil {
			s.t.Fatalf("encode generate response: %v", err)
		}
	default:
		http.NotFound(writer, request)
	}
}

func (s *testOllamaServer) URL() string {
	return s.server.URL
}

func (s *testOllamaServer) Close() {
	s.server.Close()
}

func TestServiceConnectsAndFindsMatches(t *testing.T) {
	server := newTestOllamaServer(t, `{"entities":[{"type":"PERSON_NAME","text":"Jean Dupont"}]}`)
	defer server.Close()

	service, err := NewService(context.Background(), Config{
		BaseURL:       server.URL(),
		Model:         "mistral",
		Timeout:       DefaultTimeout,
		MaxChunkBytes: 42,
	})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}
	if !server.tagsCalled {
		t.Fatal("ollama tags endpoint was not called")
	}

	matches, err := service.FindMatches(context.Background(), "Bonjour Jean Dupont")
	if err != nil {
		t.Fatalf("FindMatches returned error: %v", err)
	}
	if got, want := len(matches), 1; got != want {
		t.Fatalf("matches len = %d, want %d", got, want)
	}
	assertMatchText(t, "Bonjour Jean Dupont", matches[0], anonymizer.EntityPersonName, "Jean Dupont")
	if server.generateCalls != 2 {
		t.Fatalf("generate calls = %d, want probe and match calls", server.generateCalls)
	}
	if server.receivedModel != "mistral" {
		t.Fatalf("model = %q, want mistral", server.receivedModel)
	}

	service.Close()
}

func TestServiceReturnsProbeErrorAndCleansUp(t *testing.T) {
	server := newTestOllamaServer(t, "not-json")
	defer server.Close()

	_, err := NewService(context.Background(), Config{
		BaseURL: server.URL(),
	})
	if err == nil || !strings.Contains(err.Error(), "verify llm extractor") {
		t.Fatalf("error = %v, want LLM probe error", err)
	}
}

func TestServiceReturnsConnectionError(t *testing.T) {
	_, err := NewService(context.Background(), Config{
		BaseURL: "localhost:11434",
	})
	if err == nil || !strings.Contains(err.Error(), "connect llm") {
		t.Fatalf("error = %v, want llm connection error", err)
	}
}

func TestFindMatchesLocalizesExactAndNormalizedEntities(t *testing.T) {
	input := "Jean   Dupont vit au 12 passage Secret."
	extractor := &fakeExtractor{
		entities: []Entity{
			{Type: "PERSON_NAME", Text: "jean dupont"},
			{Type: "ADDRESS", Text: "12 passage Secret"},
			{Type: "DATE", Text: "not present"},
			{Type: "EMAIL", Text: "ignored@example.com"},
			{Type: "LOCATION", Text: "not present"},
			{Type: "OTHER_PII", Text: strings.Repeat("x", MaxEntityTextBytes+1)},
		},
	}

	matches, chunks, err := NewMatchFinder(extractor, 12000).FindMatches(context.Background(), input)
	if err != nil {
		t.Fatalf("FindMatches returned error: %v", err)
	}
	if chunks != 1 {
		t.Fatalf("chunks = %d, want 1", chunks)
	}

	if got, want := len(matches), 2; got != want {
		t.Fatalf("matches len = %d, want %d: %#v", got, want, matches)
	}
	assertMatchText(t, input, matches[0], anonymizer.EntityPersonName, "Jean   Dupont")
	assertMatchText(t, input, matches[1], anonymizer.EntityAddress, "12 passage Secret")
}

func TestFindMatchesIgnoresUnsupportedLLMEntityTypes(t *testing.T) {
	input := "Le Dr Jean-Louis Berger a validé le dossier 14AA00000 le 12 janvier 2007 pour GB-123-XT."
	extractor := &fakeExtractor{
		entities: []Entity{
			{Type: "MEDICAL_PROVIDER", Text: "Dr Jean-Louis Berger"},
			{Type: "DOCUMENT_ID", Text: "14AA00000"},
			{Type: "DATE", Text: "12 janvier 2007"},
			{Type: "VEHICLE_PLATE", Text: "GB-123-XT"},
		},
	}

	matches, chunks, err := NewMatchFinder(extractor, 12000).FindMatches(context.Background(), input)
	if err != nil {
		t.Fatalf("FindMatches returned error: %v", err)
	}
	if chunks != 1 {
		t.Fatalf("chunks = %d, want 1", chunks)
	}

	if got, want := len(matches), 2; got != want {
		t.Fatalf("matches len = %d, want %d: %#v", got, want, matches)
	}
	assertMatchText(t, input, matches[0], anonymizer.EntityDate, "12 janvier 2007")
	assertMatchText(t, input, matches[1], anonymizer.EntityVehiclePlate, "GB-123-XT")
}

func TestFindMatchesPropagatesExtractorError(t *testing.T) {
	extractor := &fakeExtractor{err: fmt.Errorf("boom")}

	_, _, err := NewMatchFinder(extractor, 12000).FindMatches(context.Background(), "text")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v, want extractor error", err)
	}
}

func TestFindMatchesSplitsInputIntoSmallerChunks(t *testing.T) {
	input := "alpha beta gamma delta epsilon"
	extractor := &fakeExtractor{}

	_, chunks, err := NewMatchFinder(extractor, 10).FindMatches(context.Background(), input)
	if err != nil {
		t.Fatalf("FindMatches returned error: %v", err)
	}
	if chunks <= 1 {
		t.Fatalf("chunks = %d, want more than 1", chunks)
	}
	if extractor.calls != chunks {
		t.Fatalf("extractor calls = %d, want %d", extractor.calls, chunks)
	}
}

func TestBestSplitPrefersPunctuationBeforeWhitespace(t *testing.T) {
	input := "alpha beta gamma. delta epsilon"

	split := bestSplit(input, 0, 25)

	if got, want := input[:split], "alpha beta gamma."; got != want {
		t.Fatalf("split chunk = %q, want %q", got, want)
	}
}

func TestOllamaExtractorBuildsPromptAndParsesEntities(t *testing.T) {
	client := &fakeOllamaClient{
		response: `{"entities":[{"type":"PERSON_NAME","text":"Jean Dupont"}]}`,
	}
	extractor, err := NewOllamaExtractor("mistral", client)
	if err != nil {
		t.Fatalf("NewOllamaExtractor returned error: %v", err)
	}

	entities, err := extractor.Extract(context.Background(), "Jean Dupont")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}

	if got, want := client.model, "mistral"; got != want {
		t.Fatalf("model = %v, want %v", got, want)
	}
	if _, ok := client.format.(piiSchema); !ok {
		t.Fatalf("format = %#v, want JSON schema object", client.format)
	}
	if !strings.Contains(client.prompt, "Return only a JSON object") {
		t.Fatalf("prompt = %#v, want strict JSON prompt", client.prompt)
	}
	for _, expected := range []string{"PERSON_NAME", "ADDRESS", "DATE", "VEHICLE_PLATE", "Dr Jean-Louis Berger", "GB-123-XT"} {
		if !strings.Contains(client.prompt, expected) {
			t.Fatalf("prompt does not contain %q:\n%s", expected, client.prompt)
		}
	}
	for _, unexpected := range []string{"DOCUMENT_ID", "MEDICAL_PROVIDER", "SCHOOL", "EMPLOYER", "PET_IDENTIFIER"} {
		if strings.Contains(client.prompt, unexpected) {
			t.Fatalf("prompt contains unsupported type %q:\n%s", unexpected, client.prompt)
		}
	}
	if got, want := entities, []Entity{{Type: "PERSON_NAME", Text: "Jean Dupont"}}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("entities = %#v, want %#v", got, want)
	}
}

func TestOllamaExtractorReturnsClientError(t *testing.T) {
	client := &fakeOllamaClient{err: fmt.Errorf("model unavailable")}
	extractor, err := NewOllamaExtractor("mistral", client)
	if err != nil {
		t.Fatalf("NewOllamaExtractor returned error: %v", err)
	}

	_, err = extractor.Extract(context.Background(), "text")
	if err == nil || !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("error = %v, want client error", err)
	}
}

func TestOllamaExtractorReturnsErrorOnInvalidModelJSON(t *testing.T) {
	client := &fakeOllamaClient{response: "not-json"}
	extractor, err := NewOllamaExtractor("mistral", client)
	if err != nil {
		t.Fatalf("NewOllamaExtractor returned error: %v", err)
	}

	_, err = extractor.Extract(context.Background(), "text")
	if err == nil || !strings.Contains(err.Error(), "parse llm JSON response") {
		t.Fatalf("error = %v, want invalid JSON error", err)
	}
}

func assertMatchText(t *testing.T, input string, match anonymizer.Match, entityType anonymizer.EntityType, text string) {
	t.Helper()

	if match.Type != entityType {
		t.Fatalf("match type = %s, want %s", match.Type, entityType)
	}
	if got := input[match.Start:match.End]; got != text {
		t.Fatalf("match text = %q, want %q", got, text)
	}
	if match.Priority != PriorityLLM {
		t.Fatalf("priority = %d, want %d", match.Priority, PriorityLLM)
	}
}
