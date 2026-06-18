package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/Korbicorp/klovis/internal/anonymizer"
)

const (
	PriorityLLM          = 50
	MaxEntityTextBytes   = 256
	DefaultMaxChunkBytes = 1000
)

type Entity struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Extractor interface {
	Extract(ctx context.Context, text []byte) ([]Entity, error)
}

type OllamaExtractor struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

func NewOllamaExtractor(baseURL, model string, timeout time.Duration) (*OllamaExtractor, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("ollama base URL is required")
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("ollama model is required")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse ollama URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("ollama URL must include scheme and host")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &OllamaExtractor{
		baseURL: strings.TrimRight(parsed.String(), "/"),
		model:   model,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

func (e *OllamaExtractor) Extract(ctx context.Context, text []byte) ([]Entity, error) {
	if e == nil {
		return nil, fmt.Errorf("nil ollama extractor")
	}

	response, err := e.generate(ctx, text, piiJSONSchema())
	if err != nil {
		response, err = e.generate(ctx, text, "json")
	}
	if err != nil {
		return nil, err
	}

	var payload extractionResponse
	decoder := json.NewDecoder(strings.NewReader(response))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("parse llm JSON response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("parse llm JSON response: trailing data")
	}

	return payload.Entities, nil
}

func (e *OllamaExtractor) generate(ctx context.Context, text []byte, format any) (string, error) {
	requestBody := ollamaGenerateRequest{
		Model:  e.model,
		Prompt: buildPrompt(text),
		Stream: false,
		Format: format,
	}

	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(requestBody); err != nil {
		return "", fmt.Errorf("encode ollama request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/api/generate", &body)
	if err != nil {
		return "", fmt.Errorf("create ollama request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := e.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("call ollama: %w", err)
	}
	defer response.Body.Close()

	rawBody, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("read ollama response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("ollama returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(rawBody)))
	}

	var payload ollamaGenerateResponse
	decoder := json.NewDecoder(bytes.NewReader(rawBody))
	if err := decoder.Decode(&payload); err != nil {
		return "", fmt.Errorf("parse ollama response: %w", err)
	}
	if payload.Response == "" {
		return "", fmt.Errorf("ollama response is empty")
	}

	return payload.Response, nil
}

func FindMatches(ctx context.Context, extractor Extractor, input []byte, maxChunkBytes int) ([]anonymizer.Match, int, error) {
	if extractor == nil {
		return nil, 0, fmt.Errorf("llm extractor is required")
	}

	chunks := splitChunks(input, maxChunkBytes)
	var matches []anonymizer.Match
	for _, chunk := range chunks {
		entities, err := extractor.Extract(ctx, chunk.text)
		if err != nil {
			return nil, len(chunks), err
		}
		matches = append(matches, matchesFromEntities(chunk.text, chunk.offset, entities)...)
	}

	return deduplicateMatches(matches), len(chunks), nil
}

func matchesFromEntities(text []byte, offset int, entities []Entity) []anonymizer.Match {
	matches := make([]anonymizer.Match, 0, len(entities))
	for _, entity := range entities {
		entityType, ok := entityTypeFromString(entity.Type)
		if !ok {
			continue
		}
		value := strings.TrimSpace(entity.Text)
		if value == "" || len(value) > MaxEntityTextBytes {
			continue
		}

		start, end, ok := locateEntity(text, value)
		if !ok {
			continue
		}

		matches = append(matches, anonymizer.Match{
			Start:      offset + start,
			End:        offset + end,
			Type:       entityType,
			Priority:   PriorityLLM,
			Normalized: normalizeLLMKey(value),
		})
	}

	return matches
}

func locateEntity(text []byte, value string) (int, int, bool) {
	if index := bytes.Index(text, []byte(value)); index >= 0 {
		return index, index + len(value), true
	}

	return locateNormalized(text, value)
}

func locateNormalized(text []byte, value string) (int, int, bool) {
	haystack, offsets := normalizeWithOffsets(text)
	needle := normalizeString(value)
	if len(needle) == 0 || len(needle) > len(haystack) {
		return 0, 0, false
	}

	start := indexRunes(haystack, needle)
	if start < 0 {
		return 0, 0, false
	}
	end := start + len(needle)

	byteStart := offsets[start]
	byteEnd := len(text)
	if end < len(offsets) {
		byteEnd = offsets[end]
	}

	return byteStart, byteEnd, true
}

func normalizeWithOffsets(text []byte) ([]rune, []int) {
	normalized := make([]rune, 0, len(text))
	offsets := make([]int, 0, len(text))
	lastWasSpace := false

	for offset, r := range string(text) {
		if unicode.IsSpace(r) {
			if lastWasSpace {
				continue
			}
			normalized = append(normalized, ' ')
			offsets = append(offsets, offset)
			lastWasSpace = true
			continue
		}

		normalized = append(normalized, unicode.ToLower(r))
		offsets = append(offsets, offset)
		lastWasSpace = false
	}

	return normalized, offsets
}

func normalizeString(value string) []rune {
	normalized, _ := normalizeWithOffsets([]byte(strings.TrimSpace(value)))
	return normalized
}

func indexRunes(haystack, needle []rune) int {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		matches := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				matches = false
				break
			}
		}
		if matches {
			return i
		}
	}

	return -1
}

func entityTypeFromString(value string) (anonymizer.EntityType, bool) {
	switch anonymizer.EntityType(strings.ToUpper(strings.TrimSpace(value))) {
	case anonymizer.EntityPersonName:
		return anonymizer.EntityPersonName, true
	case anonymizer.EntityAddress:
		return anonymizer.EntityAddress, true
	case anonymizer.EntityDate:
		return anonymizer.EntityDate, true
	default:
		return "", false
	}
}

func normalizeLLMKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

type chunk struct {
	text   []byte
	offset int
}

func splitChunks(input []byte, maxChunkBytes int) []chunk {
	if maxChunkBytes <= 0 || len(input) <= maxChunkBytes {
		return []chunk{{text: input, offset: 0}}
	}

	var chunks []chunk
	for start := 0; start < len(input); {
		end := start + maxChunkBytes
		if end >= len(input) {
			chunks = append(chunks, chunk{text: input[start:], offset: start})
			break
		}

		split := bestSplit(input, start, end)
		chunks = append(chunks, chunk{text: input[start:split], offset: start})

		start = split
	}

	return chunks
}

func bestSplit(input []byte, start, end int) int {
	min := start + ((end - start) / 2)
	for i := end; i > min; i-- {
		if isSplitPunctuation(input[i-1]) {
			return i
		}
	}

	for i := end; i > min; i-- {
		if input[i-1] == '\n' || input[i-1] == ' ' || input[i-1] == '\t' {
			return i
		}
	}

	return end
}

func isSplitPunctuation(value byte) bool {
	switch value {
	case '.', ',', ';', ':', '!', '?':
		return true
	default:
		return false
	}
}

func deduplicateMatches(matches []anonymizer.Match) []anonymizer.Match {
	seen := make(map[string]struct{}, len(matches))
	deduplicated := matches[:0]
	for _, match := range matches {
		key := fmt.Sprintf("%d:%d:%s", match.Start, match.End, match.Type)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduplicated = append(deduplicated, match)
	}

	return deduplicated
}

func buildPrompt(text []byte) string {
	return `You are a strict PII extraction engine.
Return only a JSON object with this shape:
{"entities":[{"type":"PERSON_NAME","text":"Jean Dupont"}]}

Allowed types:
- PERSON_NAME
- ADDRESS
- DATE

Rules:
- Return only strings that are exactly present in the input text.
- Do not invent values.
- Do not return emails, phone numbers, IP addresses, URLs, IBANs, credit cards, social security numbers, MAC addresses, or obvious numeric/reference IDs.
- Extract only person names, addresses, and dates.
- Include full person names and repeated short references to known people, such as first names used alone.
- Include dates tied to identity, family, documents, employment, education, health, subscriptions, payments, or events.
- Prefer the longest exact string for names and addresses, for example "Thomas Alexandre Beaumont" instead of only "Thomas Beaumont".
- Use PERSON_NAME for named people, including titles such as "Dr Jean-Louis Berger" when present.
- Use DATE for dates like "14 mars 1988", "octobre 2023", "2024", or "12 janvier 2007" when they are tied to the person profile.
- Use ADDRESS for complete address strings like "42 Rue de la République, 69002 Lyon".
- Ignore document numbers, vehicle plates, schools, employers, medical providers, locations without a full address, organizations, and other contextual identifiers.
- If nothing is found, return {"entities":[]}.

Examples:
Input: "Thomas Alexandre Beaumont est né le 14 mars 1988 à Lyon. Il réside au 42 Rue de la République, 69002 Lyon."
Output: {"entities":[{"type":"PERSON_NAME","text":"Thomas Alexandre Beaumont"},{"type":"DATE","text":"14 mars 1988"},{"type":"ADDRESS","text":"42 Rue de la République, 69002 Lyon"}]}

Input: "Son médecin traitant est le Dr Jean-Louis Berger. Il conduit une Peugeot immatriculée GB-123-XT."
Output: {"entities":[{"type":"PERSON_NAME","text":"Dr Jean-Louis Berger"}]}

Input text:
` + string(text)
}

func piiJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"entities": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"type": map[string]any{
							"type": "string",
							"enum": []string{
								string(anonymizer.EntityPersonName),
								string(anonymizer.EntityAddress),
								string(anonymizer.EntityDate),
							},
						},
						"text": map[string]any{
							"type": "string",
						},
					},
					"required":             []string{"type", "text"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"entities"},
		"additionalProperties": false,
	}
}

type extractionResponse struct {
	Entities []Entity `json:"entities"`
}

type ollamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
	Format any    `json:"format"`
}

type ollamaGenerateResponse struct {
	Response string `json:"response"`
}
