package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	ollamaclient "github.com/Korbicorp/klovis/internal/ollama/client"
	"github.com/rs/zerolog/log"
)

const (
	PriorityLLM          = 50
	MaxEntityTextBytes   = 256
	DefaultMaxChunkBytes = 1000
	DefaultBaseURL       = "http://localhost:11434"
	DefaultModel         = "mistral"
	DefaultTimeout       = 30 * time.Second
)

const piiExtractionPromptTemplate = `You are a strict PII extraction engine.
Return only a JSON object with this shape:
{"entities":[{"type":"PERSON_NAME","text":"Jean Dupont"}]}

Allowed types:
- PERSON_NAME
- ADDRESS
- DATE
- VEHICLE_PLATE

Rules:
- Return only strings that are exactly present in the input text.
- Do not invent values.
- Do not return emails, phone numbers, IP addresses, URLs, IBANs, credit cards, social security numbers, MAC addresses, or obvious numeric/reference IDs.
- Extract only person names, addresses, dates, and vehicle plates.
- Include full person names and repeated short references to known people, such as first names used alone.
- Include dates tied to identity, family, documents, employment, education, health, subscriptions, payments, or events.
- Prefer the longest exact string for names and addresses, for example "Thomas Alexandre Beaumont" instead of only "Thomas Beaumont".
- Use PERSON_NAME for named people, including titles such as "Dr Jean-Louis Berger" when present.
- Use DATE for dates like "14 mars 1988", "octobre 2023", "2024", or "12 janvier 2007" when they are tied to the person profile.
- Use ADDRESS for complete address strings like "42 Rue de la République, 69002 Lyon".
- Use VEHICLE_PLATE for vehicle registration plates like "GB-123-XT".
- Ignore document numbers, schools, employers, medical providers, locations without a full address, organizations, and other contextual identifiers.
- If nothing is found, return {"entities":[]}.

Examples:
Input: "Thomas Alexandre Beaumont est né le 14 mars 1988 à Lyon. Il réside au 42 Rue de la République, 69002 Lyon."
Output: {"entities":[{"type":"PERSON_NAME","text":"Thomas Alexandre Beaumont"},{"type":"DATE","text":"14 mars 1988"},{"type":"ADDRESS","text":"42 Rue de la République, 69002 Lyon"}]}

Input: "Son médecin traitant est le Dr Jean-Louis Berger. Il conduit une Peugeot immatriculée GB-123-XT."
Output: {"entities":[{"type":"PERSON_NAME","text":"Dr Jean-Louis Berger"},{"type":"VEHICLE_PLATE","text":"GB-123-XT"}]}

Input text:
%s`

type Entity struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Extractor interface {
	Extract(ctx context.Context, text string) ([]Entity, error)
}

type Config struct {
	BaseURL       string
	Model         string
	Timeout       time.Duration
	MaxChunkBytes int
	AutoStart     bool
}

type Service struct {
	matcher *MatchFinder
	server  io.Closer
}

func NewService(ctx context.Context, config Config) (*Service, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = DefaultModel
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxChunkBytes := config.MaxChunkBytes
	if maxChunkBytes <= 0 {
		maxChunkBytes = DefaultMaxChunkBytes
	}

	startupCtx, startupCancel := context.WithTimeout(ctx, timeout)
	server, err := EnsureOllamaServer(startupCtx, baseURL, timeout, config.AutoStart)
	startupCancel()
	if err != nil {
		return nil, fmt.Errorf("connect llm: %w", err)
	}

	client, err := ollamaclient.New(baseURL, timeout)
	if err != nil {
		_ = server.Close()
		return nil, fmt.Errorf("initialize llm extractor: %w", err)
	}
	extractor, err := NewOllamaExtractor(model, client)
	if err != nil {
		_ = server.Close()
		return nil, fmt.Errorf("initialize llm extractor: %w", err)
	}

	verifyCtx, verifyCancel := context.WithTimeout(ctx, timeout)
	if _, err := extractor.Extract(verifyCtx, "No personal data."); err != nil {
		verifyCancel()
		_ = server.Close()
		return nil, fmt.Errorf("verify llm extractor: %w", err)
	}
	verifyCancel()

	return &Service{
		matcher: NewMatchFinder(extractor, maxChunkBytes),
		server:  server,
	}, nil
}

func (s *Service) FindMatches(ctx context.Context, input string) ([]anonymizer.Match, error) {
	if s == nil || s.matcher == nil {
		return nil, fmt.Errorf("llm service is required")
	}

	matches, _, err := s.matcher.FindMatches(ctx, input)
	return matches, err
}

func (s *Service) Close() {
	if s == nil || s.server == nil {
		return
	}
	_ = s.server.Close()
}

type MatchFinder struct {
	extractor     Extractor
	maxChunkBytes int
}

func NewMatchFinder(extractor Extractor, maxChunkBytes int) *MatchFinder {
	if maxChunkBytes <= 0 {
		maxChunkBytes = DefaultMaxChunkBytes
	}
	return &MatchFinder{
		extractor:     extractor,
		maxChunkBytes: maxChunkBytes,
	}
}

type OllamaClient interface {
	Generate(ctx context.Context, model, prompt string, format any) (string, error)
}

type OllamaExtractor struct {
	model  string
	client OllamaClient
}

func NewOllamaExtractor(model string, client OllamaClient) (*OllamaExtractor, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("ollama model is required")
	}
	if client == nil {
		return nil, fmt.Errorf("ollama client is required")
	}

	return &OllamaExtractor{
		model:  model,
		client: client,
	}, nil
}

func (e *OllamaExtractor) Extract(ctx context.Context, text string) ([]Entity, error) {
	response, err := e.client.Generate(ctx, e.model, fmt.Sprintf(piiExtractionPromptTemplate, text), piiJSONSchema())
	if err != nil {
		return nil, err
	}

	var payload extractionResponse
	decoder := json.NewDecoder(strings.NewReader(response))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("parse llm JSON response: %w", err)
	}

	return payload.Entities, nil
}

func (f *MatchFinder) FindMatches(ctx context.Context, input string) ([]anonymizer.Match, int, error) {
	if f == nil || f.extractor == nil {
		return nil, 0, fmt.Errorf("llm extractor is required")
	}

	chunks := splitChunks(input, f.maxChunkBytes)
	var matches []anonymizer.Match
	for _, chunk := range chunks {
		entities, err := f.extractor.Extract(ctx, chunk.text)
		if err != nil {
			return nil, len(chunks), err
		}
		matches = append(matches, matchesFromEntities(chunk.text, chunk.start, entities)...)
	}

	return deduplicateMatches(matches), len(chunks), nil
}

func matchesFromEntities(text string, chunkStart int, entities []Entity) []anonymizer.Match {
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
			Start:      chunkStart + start,
			End:        chunkStart + end,
			Type:       entityType,
			Priority:   PriorityLLM,
			Normalized: normalizeLLMKey(value),
		})
	}

	return matches
}

func locateEntity(text string, value string) (int, int, bool) {
	pattern := entityPattern(value)
	if pattern == "" {
		return 0, 0, false
	}

	compiled, err := regexp.Compile(pattern)
	if err != nil {
		log.Error().Err(err).Msg("compile llm entity locator regex")
		return 0, 0, false
	}

	match := compiled.FindStringIndex(text)
	if match == nil {
		return 0, 0, false
	}

	return match[0], match[1], true
}

func entityPattern(value string) string {
	parts := strings.Fields(value)
	if len(parts) == 0 {
		return ""
	}
	for index, part := range parts {
		parts[index] = regexp.QuoteMeta(part)
	}

	return `(?i)` + strings.Join(parts, `\s+`)
}

func entityTypeFromString(value string) (anonymizer.EntityType, bool) {
	switch anonymizer.EntityType(strings.ToUpper(strings.TrimSpace(value))) {
	case anonymizer.EntityPersonName:
		return anonymizer.EntityPersonName, true
	case anonymizer.EntityAddress:
		return anonymizer.EntityAddress, true
	case anonymizer.EntityDate:
		return anonymizer.EntityDate, true
	case anonymizer.EntityVehiclePlate:
		return anonymizer.EntityVehiclePlate, true
	default:
		return "", false
	}
}

func normalizeLLMKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

type chunk struct {
	text  string
	start int
}

func splitChunks(input string, maxChunkBytes int) []chunk {
	if maxChunkBytes <= 0 || len(input) <= maxChunkBytes {
		return []chunk{{text: input, start: 0}}
	}

	var chunks []chunk
	for start := 0; start < len(input); {
		end := start + maxChunkBytes
		if end >= len(input) {
			chunks = append(chunks, chunk{text: input[start:], start: start})
			break
		}

		split := bestSplit(input, start, end)
		chunks = append(chunks, chunk{text: input[start:split], start: start})

		start = split
	}

	return chunks
}

func bestSplit(input string, start, end int) int {
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
	seen := make(map[llmMatchKey]struct{}, len(matches))
	deduplicated := matches[:0]
	for _, match := range matches {
		key := llmMatchKey{start: match.Start, end: match.End, entityType: match.Type}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduplicated = append(deduplicated, match)
	}

	return deduplicated
}

type llmMatchKey struct {
	start      int
	end        int
	entityType anonymizer.EntityType
}

func piiJSONSchema() piiSchema {
	noExtraFields := false
	return piiSchema{
		Type: "object",
		Properties: piiSchemaProperties{
			Entities: arraySchema{
				Type: "array",
				Items: objectSchema{
					Type: "object",
					Properties: entitySchemaProperties{
						Type: enumSchema{
							Type: "string",
							Enum: []string{
								string(anonymizer.EntityPersonName),
								string(anonymizer.EntityAddress),
								string(anonymizer.EntityDate),
								string(anonymizer.EntityVehiclePlate),
							},
						},
						Text: scalarSchema{
							Type: "string",
						},
					},
					Required:             []string{"type", "text"},
					AdditionalProperties: &noExtraFields,
				},
			},
		},
		Required:             []string{"entities"},
		AdditionalProperties: &noExtraFields,
	}
}

type extractionResponse struct {
	Entities []Entity `json:"entities"`
}

type piiSchema struct {
	Type                 string              `json:"type"`
	Properties           piiSchemaProperties `json:"properties"`
	Required             []string            `json:"required"`
	AdditionalProperties *bool               `json:"additionalProperties"`
}

type piiSchemaProperties struct {
	Entities arraySchema `json:"entities"`
}

type arraySchema struct {
	Type  string       `json:"type"`
	Items objectSchema `json:"items"`
}

type objectSchema struct {
	Type                 string                 `json:"type"`
	Properties           entitySchemaProperties `json:"properties"`
	Required             []string               `json:"required"`
	AdditionalProperties *bool                  `json:"additionalProperties"`
}

type entitySchemaProperties struct {
	Type enumSchema   `json:"type"`
	Text scalarSchema `json:"text"`
}

type enumSchema struct {
	Type string   `json:"type"`
	Enum []string `json:"enum"`
}

type scalarSchema struct {
	Type string `json:"type"`
}
