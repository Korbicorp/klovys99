package ner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
)

var ErrAnalysis = errors.New("local NER analysis failed")

// MatchSet stores one analysis result per deduplicated input string.
type MatchSet map[string][]anonymizer.Match

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
	results, err := analyzer.AnalyzeBatch(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAnalysis, err)
	}
	if len(results) != len(texts) {
		return nil, fmt.Errorf("%w: result count mismatch", ErrAnalysis)
	}
	matchSet := make(MatchSet, len(texts))
	for index, text := range texts {
		matchSet[text] = results[index]
	}
	return matchSet, nil
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
