package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/Korbicorp/klovys99/internal/fileanonymizer"
	"github.com/Korbicorp/klovys99/internal/ner"
	"github.com/rs/zerolog"
)

func TestParallelRequestPreparationRunsNERAndFilesInParallel(t *testing.T) {
	t.Helper()

	blocker := newParallelBlocker()
	preparation, err := parallelRequestPreparation(
		context.Background(),
		"openai",
		map[string]any{
			"input": []any{
				map[string]any{"type": "input_text", "text": "hello"},
				map[string]any{
					"type":      "input_file",
					"filename":  "doc.bin",
					"file_data": base64.StdEncoding.EncodeToString([]byte("raw")),
				},
			},
		},
		[]string{"hello"},
		&blockingAnalyzer{blocker: blocker},
		&stubFileAnonymizer{
			enabled:       true,
			failurePolicy: fileanonymizer.PolicyReject,
			blocker:       blocker,
			anonymize: func(context.Context, string, string, []byte) ([]byte, anonymizer.Result, error) {
				return []byte("safe"), anonymizer.Result{Stats: map[anonymizer.EntityType]anonymizer.EntityStats{}}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("parallelRequestPreparation: %v", err)
	}
	if !preparation.filesChanged {
		t.Fatalf("filesChanged = false, want true")
	}
	if blocker.nerStarted == 0 || blocker.fileStarted == 0 {
		t.Fatalf("parallel branches did not both start: ner=%d file=%d", blocker.nerStarted, blocker.fileStarted)
	}
}

func TestParallelRequestPreparationPrioritizesNERFailure(t *testing.T) {
	t.Helper()

	nerFailure := errors.New("ner failed")
	fileFailure := errors.New("file failed")
	_, err := parallelRequestPreparation(
		context.Background(),
		"openai",
		map[string]any{
			"input": []any{
				map[string]any{"type": "input_text", "text": "hello"},
				map[string]any{
					"type":      "input_file",
					"filename":  "doc.bin",
					"file_data": base64.StdEncoding.EncodeToString([]byte("raw")),
				},
			},
		},
		[]string{"hello"},
		stubAnalyzer{err: nerFailure},
		&stubFileAnonymizer{
			enabled:       true,
			failurePolicy: fileanonymizer.PolicyReject,
			anonymize: func(context.Context, string, string, []byte) ([]byte, anonymizer.Result, error) {
				return nil, anonymizer.Result{}, fileFailure
			},
		},
	)
	if !errors.Is(err, ner.ErrAnalysis) || !strings.Contains(err.Error(), nerFailure.Error()) {
		t.Fatalf("error = %v, want wrapped ner failure containing %q", err, nerFailure)
	}
}

func TestAnonymizeInlineFilesRespectsFailurePolicies(t *testing.T) {
	tests := []struct {
		name           string
		policy         string
		wantChanged    bool
		wantItemCount  int
		wantErrSubstr  string
		wantFileRetain bool
	}{
		{name: "passthrough", policy: fileanonymizer.PolicyPassthrough, wantChanged: false, wantItemCount: 2, wantFileRetain: true},
		{name: "remove", policy: fileanonymizer.PolicyRemove, wantChanged: true, wantItemCount: 1, wantFileRetain: false},
		{name: "reject", policy: fileanonymizer.PolicyReject, wantErrSubstr: "boom", wantItemCount: 2, wantFileRetain: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]any{
				"input": []any{
					map[string]any{"type": "input_text", "text": "hello"},
					map[string]any{
						"type":      "input_file",
						"filename":  "doc.bin",
						"file_data": base64.StdEncoding.EncodeToString([]byte("raw")),
					},
				},
			}
			changed, _, err := anonymizeInlineFiles(context.Background(), "openai", &stubFileAnonymizer{
				enabled:       true,
				failurePolicy: tt.policy,
				anonymize: func(context.Context, string, string, []byte) ([]byte, anonymizer.Result, error) {
					return nil, anonymizer.Result{}, errors.New("boom")
				},
			}, payload)
			if tt.wantErrSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErrSubstr)
				}
			} else if err != nil {
				t.Fatalf("anonymizeInlineFiles: %v", err)
			}
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tt.wantChanged)
			}

			items, ok := payload["input"].([]any)
			if !ok {
				t.Fatalf("input type = %T, want []any", payload["input"])
			}
			if len(items) != tt.wantItemCount {
				t.Fatalf("item count = %d, want %d", len(items), tt.wantItemCount)
			}
			hasFile := false
			for _, item := range items {
				if object, ok := item.(map[string]any); ok && stringMapValue(object, "type") == "input_file" {
					hasFile = true
				}
			}
			if hasFile != tt.wantFileRetain {
				t.Fatalf("file retained = %v, want %v", hasFile, tt.wantFileRetain)
			}
		})
	}
}

func TestOpenAIAnonymizeResponsesBodyPreservesTokenOrderWithInlineFiles(t *testing.T) {
	engine := anonymizer.NewService([]anonymizer.Detector{
		literalDetector{entityType: anonymizer.EntityEmail, value: "alice@example.com"},
		literalDetector{entityType: anonymizer.EntityEmail, value: "file@example.com"},
	})
	provider, err := NewOpenAI(OpenAIConfig{
		APITarget:   mustURL(t, "https://api.openai.com"),
		HTTPClient:  nil,
		Anonymizer:  engine,
		Logger:      ptrLogger(zerolog.Nop()),
		NERAnalyzer: nil,
		FileAnonymizer: &stubFileAnonymizer{
			enabled:       true,
			failurePolicy: fileanonymizer.PolicyReject,
			textEngine:    engine,
		},
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}

	body := `{"input":[{"role":"user","content":[{"type":"input_text","text":"Email alice@example.com"},{"type":"input_file","filename":"note.txt","file_data":"` +
		base64.StdEncoding.EncodeToString([]byte("file@example.com")) + `"}]}]}`
	result, err := provider.anonymizeResponsesBody(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("anonymizeResponsesBody: %v", err)
	}

	payload := decodeBody(t, result.Body)
	input := payload["input"].([]any)
	user := input[0].(map[string]any)
	content := user["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	fileData := content[1].(map[string]any)["file_data"].(string)
	decodedFile, err := base64.StdEncoding.DecodeString(fileData)
	if err != nil {
		t.Fatalf("decode file data: %v", err)
	}
	if text != "Email [EMAIL_2]" {
		t.Fatalf("text = %q, want %q", text, "Email [EMAIL_2]")
	}
	if got := string(decodedFile); got != "[EMAIL_1]" {
		t.Fatalf("file data = %q, want %q", got, "[EMAIL_1]")
	}
}

func TestOpenAIAnonymizeChatCompletionsBodyPreservesTokenOrderWithInlineFiles(t *testing.T) {
	engine := anonymizer.NewService([]anonymizer.Detector{
		literalDetector{entityType: anonymizer.EntityEmail, value: "alice@example.com"},
		literalDetector{entityType: anonymizer.EntityEmail, value: "file@example.com"},
	})
	provider, err := NewOpenAI(OpenAIConfig{
		APITarget:  mustURL(t, "https://api.openai.com"),
		Anonymizer: engine,
		Logger:     ptrLogger(zerolog.Nop()),
		FileAnonymizer: &stubFileAnonymizer{
			enabled:       true,
			failurePolicy: fileanonymizer.PolicyReject,
			textEngine:    engine,
		},
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}

	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"Email alice@example.com"},{"type":"input_file","filename":"note.txt","file_data":"` +
		base64.StdEncoding.EncodeToString([]byte("file@example.com")) + `"}]}]}`
	result, err := provider.anonymizeChatCompletionsBody(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("anonymizeChatCompletionsBody: %v", err)
	}

	payload := decodeBody(t, result.Body)
	messages := payload["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	fileData := content[1].(map[string]any)["file_data"].(string)
	decodedFile, err := base64.StdEncoding.DecodeString(fileData)
	if err != nil {
		t.Fatalf("decode file data: %v", err)
	}
	if text != "Email [EMAIL_1]" {
		t.Fatalf("text = %q, want %q", text, "Email [EMAIL_1]")
	}
	if got := string(decodedFile); got != "[EMAIL_1]" {
		t.Fatalf("file data = %q, want %q", got, "[EMAIL_1]")
	}
}

func TestOpenAIAnonymizeResponsesBodyDoesNotTextAnonymizeInlineImageDataURL(t *testing.T) {
	engine := anonymizer.NewService([]anonymizer.Detector{
		literalDetector{entityType: anonymizer.EntityCrypto, value: "AAAA"},
	})
	provider, err := NewOpenAI(OpenAIConfig{
		APITarget:   mustURL(t, "https://api.openai.com"),
		Anonymizer:  engine,
		Logger:      ptrLogger(zerolog.Nop()),
		NERAnalyzer: nil,
		FileAnonymizer: &stubFileAnonymizer{
			enabled:       true,
			failurePolicy: fileanonymizer.PolicyReject,
			anonymize: func(_ context.Context, gotProvider, gotMediaType string, data []byte) ([]byte, anonymizer.Result, error) {
				if gotProvider != "openai" {
					t.Fatalf("provider = %q, want %q", gotProvider, "openai")
				}
				if gotMediaType != "image/png" {
					t.Fatalf("mediaType = %q, want %q", gotMediaType, "image/png")
				}
				if string(data) != "AAAA" {
					t.Fatalf("decoded data = %q, want %q", string(data), "AAAA")
				}
				return []byte("BBBB"), anonymizer.Result{Stats: map[anonymizer.EntityType]anonymizer.EntityStats{}}, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}

	body := `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,QUFBQQ=="}]}]}`
	result, err := provider.anonymizeResponsesBody(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("anonymizeResponsesBody: %v", err)
	}

	payload := decodeBody(t, result.Body)
	input := payload["input"].([]any)
	user := input[0].(map[string]any)
	content := user["content"].([]any)
	imageURL := content[0].(map[string]any)["image_url"].(string)
	if imageURL != "data:image/png;base64,QkJCQg==" {
		t.Fatalf("image_url = %q, want %q", imageURL, "data:image/png;base64,QkJCQg==")
	}
}

func TestOpenAIAnonymizeWebSocketFrameDoesNotTextAnonymizeInlineImageDataURL(t *testing.T) {
	engine := anonymizer.NewService([]anonymizer.Detector{
		literalDetector{entityType: anonymizer.EntityCrypto, value: "AAAA"},
	})
	provider, err := NewOpenAI(OpenAIConfig{
		APITarget:   mustURL(t, "https://api.openai.com"),
		Anonymizer:  engine,
		Logger:      ptrLogger(zerolog.Nop()),
		NERAnalyzer: nil,
		FileAnonymizer: &stubFileAnonymizer{
			enabled:       true,
			failurePolicy: fileanonymizer.PolicyReject,
			anonymize: func(_ context.Context, gotProvider, gotMediaType string, data []byte) ([]byte, anonymizer.Result, error) {
				if gotProvider != "openai" {
					t.Fatalf("provider = %q, want %q", gotProvider, "openai")
				}
				if gotMediaType != "image/png" {
					t.Fatalf("mediaType = %q, want %q", gotMediaType, "image/png")
				}
				if string(data) != "AAAA" {
					t.Fatalf("decoded data = %q, want %q", string(data), "AAAA")
				}
				return []byte("BBBB"), anonymizer.Result{Stats: map[anonymizer.EntityType]anonymizer.EntityStats{}}, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}

	body := `{"type":"response.create","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,QUFBQQ=="}]}]}`
	_, output, _, _, err := provider.anonymizeWebSocketFrame(nil, []byte(body))
	if err != nil {
		t.Fatalf("anonymizeWebSocketFrame: %v", err)
	}

	payload := decodeBody(t, output)
	input := payload["input"].([]any)
	user := input[0].(map[string]any)
	content := user["content"].([]any)
	imageURL := content[0].(map[string]any)["image_url"].(string)
	if imageURL != "data:image/png;base64,QkJCQg==" {
		t.Fatalf("image_url = %q, want %q", imageURL, "data:image/png;base64,QkJCQg==")
	}
}

func TestAnthropicAnonymizePreservesTokenOrderWithInlineFiles(t *testing.T) {
	engine := anonymizer.NewService([]anonymizer.Detector{
		literalDetector{entityType: anonymizer.EntityEmail, value: "alice@example.com"},
		literalDetector{entityType: anonymizer.EntityEmail, value: "file@example.com"},
	})
	provider, err := NewAnthropic(AnthropicConfig{
		Anonymizer: engine,
		FileAnonymizer: &stubFileAnonymizer{
			enabled:       true,
			failurePolicy: fileanonymizer.PolicyReject,
			textEngine:    engine,
		},
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}

	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"Email alice@example.com"},{"type":"document","source":{"type":"base64","media_type":"text/plain","data":"` +
		base64.StdEncoding.EncodeToString([]byte("file@example.com")) + `"}}]}]}`
	result, err := provider.anonymize(context.Background(), zerolog.Nop(), []byte(body), false)
	if err != nil {
		t.Fatalf("anonymize: %v", err)
	}

	payload := decodeBody(t, result.Body)
	messages := payload["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	source := content[1].(map[string]any)["source"].(map[string]any)
	decodedFile, err := base64.StdEncoding.DecodeString(source["data"].(string))
	if err != nil {
		t.Fatalf("decode file data: %v", err)
	}
	if text != "Email [EMAIL_1]" {
		t.Fatalf("text = %q, want %q", text, "Email [EMAIL_1]")
	}
	if got := string(decodedFile); got != "[EMAIL_1]" {
		t.Fatalf("file data = %q, want %q", got, "[EMAIL_1]")
	}
}

type stubAnalyzer struct {
	err     error
	matches map[string][]anonymizer.Match
}

func (s stubAnalyzer) AnalyzeBatch(_ context.Context, texts []string) ([][]anonymizer.Match, error) {
	if s.err != nil {
		return nil, s.err
	}
	results := make([][]anonymizer.Match, len(texts))
	for index, text := range texts {
		results[index] = append([]anonymizer.Match(nil), s.matches[text]...)
	}
	return results, nil
}

func (s stubAnalyzer) Status() ner.Status {
	return ner.Status{Enabled: true, State: "ready"}
}

type stubFileAnonymizer struct {
	enabled       bool
	failurePolicy string
	textEngine    *anonymizer.Service
	blocker       *parallelBlocker
	anonymize     func(context.Context, string, string, []byte) ([]byte, anonymizer.Result, error)
}

func (s *stubFileAnonymizer) Enabled() bool {
	return s != nil && s.enabled
}

func (s *stubFileAnonymizer) FailurePolicy() string {
	if s == nil || s.failurePolicy == "" {
		return fileanonymizer.PolicyPassthrough
	}
	return s.failurePolicy
}

func (s *stubFileAnonymizer) Anonymize(ctx context.Context, provider, mediaType string, data []byte) ([]byte, anonymizer.Result, error) {
	if s.blocker != nil {
		if err := s.blocker.onFileStart(); err != nil {
			return nil, anonymizer.Result{}, err
		}
	}
	if s.anonymize != nil {
		return s.anonymize(ctx, provider, mediaType, data)
	}
	return data, anonymizer.Result{Stats: map[anonymizer.EntityType]anonymizer.EntityStats{}}, nil
}

func (s *stubFileAnonymizer) AnonymizePlainText(data []byte) ([]byte, anonymizer.Result) {
	if s.textEngine == nil {
		return data, anonymizer.Result{Stats: map[anonymizer.EntityType]anonymizer.EntityStats{}}
	}
	run := s.textEngine.NewRun()
	defer run.Close()
	output, result := run.Anonymize(string(data))
	return []byte(output), result
}

func (s *stubFileAnonymizer) RegisterFileID(string, string) {}

func (s *stubFileAnonymizer) IsTrustedFileID(string, string) bool { return false }

type blockingAnalyzer struct {
	blocker *parallelBlocker
}

func (b *blockingAnalyzer) AnalyzeBatch(_ context.Context, texts []string) ([][]anonymizer.Match, error) {
	if err := b.blocker.onNERStart(); err != nil {
		return nil, err
	}
	return make([][]anonymizer.Match, len(texts)), nil
}

func (b *blockingAnalyzer) Status() ner.Status {
	return ner.Status{Enabled: true, State: "ready"}
}

type parallelBlocker struct {
	nerStarted  int
	fileStarted int
	nerSignal   chan struct{}
	fileSignal  chan struct{}
}

func newParallelBlocker() *parallelBlocker {
	return &parallelBlocker{
		nerSignal:  make(chan struct{}),
		fileSignal: make(chan struct{}),
	}
}

func (b *parallelBlocker) onNERStart() error {
	b.nerStarted++
	select {
	case <-b.nerSignal:
	default:
		close(b.nerSignal)
	}
	select {
	case <-b.fileSignal:
		return nil
	case <-time.After(200 * time.Millisecond):
		return errors.New("file branch did not start in parallel")
	}
}

func (b *parallelBlocker) onFileStart() error {
	b.fileStarted++
	select {
	case <-b.fileSignal:
	default:
		close(b.fileSignal)
	}
	select {
	case <-b.nerSignal:
		return nil
	case <-time.After(200 * time.Millisecond):
		return errors.New("ner branch did not start in parallel")
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

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsed
}

func ptrLogger(logger zerolog.Logger) *zerolog.Logger {
	return &logger
}

func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return payload
}
