// Command ner_eval evaluates regex-only, GLiNER-only, and hybrid exact spans.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/Korbicorp/klovys99/internal/detectors"
	"github.com/Korbicorp/klovys99/internal/ner"
)

type corpusCase struct {
	ID       string             `json:"id"`
	Language string             `json:"language"`
	Text     string             `json:"text"`
	Spans    []anonymizer.Match `json:"spans"`
	Critical bool               `json:"critical"`
}

type score struct {
	TruePositive  int     `json:"true_positive"`
	FalsePositive int     `json:"false_positive"`
	FalseNegative int     `json:"false_negative"`
	Precision     float64 `json:"precision"`
	Recall        float64 `json:"recall"`
	F1            float64 `json:"f1"`
	LatencyMS     int64   `json:"latency_ms"`
	CriticalLeaks int     `json:"critical_leaks"`
}

func main() {
	cases, err := loadCorpus("test/ner_eval/corpus.jsonl")
	if err != nil {
		panic(err)
	}
	regex := anonymizer.NewService(detectors.Default(true))
	var analyzer ner.Analyzer
	if os.Getenv("KLOVIS_GLINER_ENABLED") == "true" {
		analyzer, err = ner.NewClient(ner.Config{
			URL: os.Getenv("KLOVIS_GLINER_URL"), Model: os.Getenv("KLOVIS_GLINER_MODEL"),
			ModelRevision: os.Getenv("KLOVIS_GLINER_MODEL_REVISION"), Timeout: 30 * time.Second,
		})
		if err != nil {
			panic(err)
		}
	}
	report := map[string]score{"regex-only": evaluate(cases, regex, nil, false)}
	if analyzer != nil {
		report["gliner-only"] = evaluate(cases, nil, analyzer, false)
		report["hybrid"] = evaluate(cases, regex, analyzer, true)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		panic(err)
	}
}

func evaluate(cases []corpusCase, service *anonymizer.Service, analyzer ner.Analyzer, hybrid bool) score {
	started := time.Now()
	var result score
	for _, item := range cases {
		var predicted []anonymizer.Match
		if service != nil {
			preview := service.Preview(item.Text)
			for _, finding := range preview.Findings {
				predicted = append(predicted, anonymizer.Match{Start: finding.Start, End: finding.End, Type: finding.Type})
			}
		}
		if analyzer != nil {
			matches, err := analyzer.AnalyzeBatch(context.Background(), []string{item.Text})
			if err != nil {
				panic(err)
			}
			if hybrid && service != nil {
				preview := service.PreviewWithMatches(item.Text, matches[0])
				predicted = predicted[:0]
				for _, finding := range preview.Findings {
					predicted = append(predicted, anonymizer.Match{Start: finding.Start, End: finding.End, Type: finding.Type})
				}
			} else {
				predicted = matches[0]
			}
		}
		tp, fp, fn := compare(predicted, item.Spans)
		result.TruePositive += tp
		result.FalsePositive += fp
		result.FalseNegative += fn
		if item.Critical && fn > 0 {
			result.CriticalLeaks++
		}
	}
	result.LatencyMS = time.Since(started).Milliseconds()
	result.Precision = ratio(result.TruePositive, result.TruePositive+result.FalsePositive)
	result.Recall = ratio(result.TruePositive, result.TruePositive+result.FalseNegative)
	if result.Precision+result.Recall > 0 {
		result.F1 = 2 * result.Precision * result.Recall / (result.Precision + result.Recall)
	}
	return result
}

func compare(actual, expected []anonymizer.Match) (int, int, int) {
	keys := func(matches []anonymizer.Match) map[string]struct{} {
		values := make(map[string]struct{}, len(matches))
		for _, match := range matches {
			values[fmt.Sprintf("%d:%d:%s", match.Start, match.End, match.Type)] = struct{}{}
		}
		return values
	}
	a, e := keys(actual), keys(expected)
	tp := 0
	for key := range a {
		if _, ok := e[key]; ok {
			tp++
		}
	}
	return tp, len(a) - tp, len(e) - tp
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 1
	}
	return float64(a) / float64(b)
}

func loadCorpus(path string) ([]corpusCase, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var cases []corpusCase
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		var item corpusCase
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, err
		}
		if item.ID == "" || item.Text == "" || (item.Language != "en" && item.Language != "fr") {
			return nil, fmt.Errorf("invalid corpus case")
		}
		for _, span := range item.Spans {
			if span.Start < 0 || span.End > len(item.Text) || span.Start >= span.End || span.Type == "" {
				return nil, fmt.Errorf("invalid byte span in %s", item.ID)
			}
		}
		cases = append(cases, item)
	}
	sort.Slice(cases, func(i, j int) bool { return strings.Compare(cases[i].ID, cases[j].ID) < 0 })
	return cases, scanner.Err()
}
