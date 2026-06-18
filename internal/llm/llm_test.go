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

func (f *fakeExtractor) Extract(context.Context, []byte) ([]Entity, error) {
	f.calls++
	return f.entities, f.err
}

func TestFindMatchesLocalizesExactAndNormalizedEntities(t *testing.T) {
	input := []byte("Jean   Dupont vit au 12 passage Secret.")
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

	matches, chunks, err := FindMatches(context.Background(), extractor, input, 12000)
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
	input := []byte("Le Dr Jean-Louis Berger a validé le dossier 14AA00000 le 12 janvier 2007 pour GB-123-XT.")
	extractor := &fakeExtractor{
		entities: []Entity{
			{Type: "MEDICAL_PROVIDER", Text: "Dr Jean-Louis Berger"},
			{Type: "DOCUMENT_ID", Text: "14AA00000"},
			{Type: "DATE", Text: "12 janvier 2007"},
			{Type: "VEHICLE_PLATE", Text: "GB-123-XT"},
		},
	}

	matches, chunks, err := FindMatches(context.Background(), extractor, input, 12000)
	if err != nil {
		t.Fatalf("FindMatches returned error: %v", err)
	}
	if chunks != 1 {
		t.Fatalf("chunks = %d, want 1", chunks)
	}

	if got, want := len(matches), 1; got != want {
		t.Fatalf("matches len = %d, want %d: %#v", got, want, matches)
	}
	assertMatchText(t, input, matches[0], anonymizer.EntityDate, "12 janvier 2007")
}

func TestFindMatchesPropagatesExtractorError(t *testing.T) {
	extractor := &fakeExtractor{err: fmt.Errorf("boom")}

	_, _, err := FindMatches(context.Background(), extractor, []byte("text"), 12000)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v, want extractor error", err)
	}
}

func TestFindMatchesSplitsInputIntoSmallerChunks(t *testing.T) {
	input := []byte("alpha beta gamma delta epsilon")
	extractor := &fakeExtractor{}

	_, chunks, err := FindMatches(context.Background(), extractor, input, 10)
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
	input := []byte("alpha beta gamma. delta epsilon")

	split := bestSplit(input, 0, 25)

	if got, want := string(input[:split]), "alpha beta gamma."; got != want {
		t.Fatalf("split chunk = %q, want %q", got, want)
	}
}

func TestOllamaExtractorSendsGenerateRequestAndParsesEntities(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/generate" {
			t.Fatalf("path = %q, want /api/generate", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		fmt.Fprint(writer, `{"response":"{\"entities\":[{\"type\":\"PERSON_NAME\",\"text\":\"Jean Dupont\"}]}","done":true}`)
	}))
	defer server.Close()

	extractor, err := NewOllamaExtractor(server.URL, "mistral", 0)
	if err != nil {
		t.Fatalf("NewOllamaExtractor returned error: %v", err)
	}

	entities, err := extractor.Extract(context.Background(), []byte("Jean Dupont"))
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}

	if got, want := received["model"], "mistral"; got != want {
		t.Fatalf("model = %v, want %v", got, want)
	}
	if got, want := received["stream"], false; got != want {
		t.Fatalf("stream = %v, want %v", got, want)
	}
	if _, ok := received["format"].(map[string]any); !ok {
		t.Fatalf("format = %#v, want JSON schema object", received["format"])
	}
	prompt, ok := received["prompt"].(string)
	if !ok || !strings.Contains(prompt, "Return only a JSON object") {
		t.Fatalf("prompt = %#v, want strict JSON prompt", received["prompt"])
	}
	for _, expected := range []string{"PERSON_NAME", "ADDRESS", "DATE", "Dr Jean-Louis Berger"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt does not contain %q:\n%s", expected, prompt)
		}
	}
	for _, unexpected := range []string{"DOCUMENT_ID", "VEHICLE_PLATE", "MEDICAL_PROVIDER", "SCHOOL", "EMPLOYER", "PET_IDENTIFIER"} {
		if strings.Contains(prompt, unexpected) {
			t.Fatalf("prompt contains unsupported type %q:\n%s", unexpected, prompt)
		}
	}
	if got, want := entities, []Entity{{Type: "PERSON_NAME", Text: "Jean Dupont"}}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("entities = %#v, want %#v", got, want)
	}
}

func TestOllamaExtractorFallsBackToJSONModeWhenSchemaFails(t *testing.T) {
	calls := 0
	var secondFormat any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var received map[string]any
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if calls == 1 {
			http.Error(writer, "schema unsupported", http.StatusBadRequest)
			return
		}
		secondFormat = received["format"]
		fmt.Fprint(writer, `{"response":"{\"entities\":[]}"}`)
	}))
	defer server.Close()

	extractor, err := NewOllamaExtractor(server.URL, "mistral", 0)
	if err != nil {
		t.Fatalf("NewOllamaExtractor returned error: %v", err)
	}

	_, err = extractor.Extract(context.Background(), []byte("nothing"))
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if got, want := secondFormat, "json"; got != want {
		t.Fatalf("fallback format = %#v, want %q", got, want)
	}
}

func TestOllamaExtractorReturnsErrorOnInvalidModelJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"response":"not-json"}`)
	}))
	defer server.Close()

	extractor, err := NewOllamaExtractor(server.URL, "mistral", 0)
	if err != nil {
		t.Fatalf("NewOllamaExtractor returned error: %v", err)
	}

	_, err = extractor.Extract(context.Background(), []byte("text"))
	if err == nil || !strings.Contains(err.Error(), "parse llm JSON response") {
		t.Fatalf("error = %v, want invalid JSON error", err)
	}
}

func TestOllamaExtractorReturnsErrorWhenHTTPFailsTwice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	extractor, err := NewOllamaExtractor(server.URL, "mistral", 0)
	if err != nil {
		t.Fatalf("NewOllamaExtractor returned error: %v", err)
	}

	_, err = extractor.Extract(context.Background(), []byte("text"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("error = %v, want HTTP 500 error", err)
	}
}

func assertMatchText(t *testing.T, input []byte, match anonymizer.Match, entityType anonymizer.EntityType, text string) {
	t.Helper()

	if match.Type != entityType {
		t.Fatalf("match type = %s, want %s", match.Type, entityType)
	}
	if got := string(input[match.Start:match.End]); got != text {
		t.Fatalf("match text = %q, want %q", got, text)
	}
	if match.Priority != PriorityLLM {
		t.Fatalf("priority = %d, want %d", match.Priority, PriorityLLM)
	}
}
