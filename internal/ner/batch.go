package ner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/rs/zerolog/log"
)

var ErrAnalysis = errors.New("local NER analysis failed")

// MatchSet stores one analysis result per deduplicated input string.
type MatchSet map[string][]anonymizer.Match

// AnalyzeStrings deduplicates explicit text inputs, then performs one analyzer call.
func AnalyzeStrings(ctx context.Context, analyzer Analyzer, texts []string) (MatchSet, error) {
	if analyzer == nil {
		return nil, nil
	}
	start := time.Now()
	log.Debug().
		Str("step", "ner_batch_analysis").
		Int("input_count", len(texts)).
		Msg("debug step started")
	seen := make(map[string]struct{}, len(texts))
	deduped := make([]string, 0, len(texts))
	for _, text := range texts {
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		deduped = append(deduped, text)
	}
	results, err := analyzer.AnalyzeBatch(ctx, deduped)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAnalysis, err)
	}
	if len(results) != len(deduped) {
		return nil, fmt.Errorf("%w: result count mismatch", ErrAnalysis)
	}
	matchSet := make(MatchSet, len(deduped))
	for index, text := range deduped {
		matchSet[text] = results[index]
	}
	log.Debug().
		Str("step", "ner_batch_analysis").
		Int("input_count", len(texts)).
		Int("deduped_count", len(deduped)).
		Dur("elapsed", time.Since(start)).
		Msg("debug step finished")
	return matchSet, nil
}

// AnalyzeJSONStrings extracts and deduplicates every JSON string, then performs
// one analyzer call. Providers consume matches only for fields they already
// consider eligible for anonymization.
func AnalyzeJSONStrings(ctx context.Context, analyzer Analyzer, body []byte) (MatchSet, error) {
	if analyzer == nil {
		return nil, nil
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode request for local NER: %w", err)
	}
	seen := make(map[string]struct{})
	var texts []string
	collectStrings(payload, seen, &texts)
	return AnalyzeStrings(ctx, analyzer, texts)
}

func collectStrings(value any, seen map[string]struct{}, texts *[]string) {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return
		}
		if _, ok := seen[typed]; ok {
			return
		}
		seen[typed] = struct{}{}
		*texts = append(*texts, typed)
	case []any:
		for _, item := range typed {
			collectStrings(item, seen, texts)
		}
	case map[string]any:
		for _, item := range typed {
			collectStrings(item, seen, texts)
		}
	}
}
