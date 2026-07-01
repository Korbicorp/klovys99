package detectors

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	blconfig "github.com/betterleaks/betterleaks/config"
	bldetect "github.com/betterleaks/betterleaks/detect"
	blreport "github.com/betterleaks/betterleaks/report"
	"github.com/betterleaks/betterleaks/sources"
)

const DefaultGitleaksTimeout = 10 * time.Second

type betterleaksDetector struct {
	detector *bldetect.Detector
	mu       sync.Mutex
}

func LoadDefaultGitleaksRules(ctx context.Context) ([]anonymizer.Detector, error) {
	return LoadGitleaksRules(ctx, "", DefaultGitleaksTimeout)
}

func LoadGitleaksRules(ctx context.Context, _ string, timeout time.Duration) ([]anonymizer.Detector, error) {
	result, err := LoadGitleaksRulesWithStats(ctx, "", timeout)
	if err != nil {
		return nil, err
	}
	return result.Detectors, nil
}

func LoadGitleaksRulesWithStats(ctx context.Context, _ string, timeout time.Duration) (ExternalRuleLoadResult, error) {
	totalStart := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = DefaultGitleaksTimeout
	}

	loadCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	parseStart := time.Now()
	config, err := blconfig.Default()
	if err != nil {
		return ExternalRuleLoadResult{}, fmt.Errorf("load betterleaks default config: %w", err)
	}

	metrics := ExternalLoadMetrics{
		Parse: time.Since(parseStart),
		Rules: len(config.Rules),
	}

	compileStart := time.Now()
	detectors, err := detectorsFromBetterleaksConfig(loadCtx, config)
	metrics.Compile = time.Since(compileStart)
	if err != nil {
		return ExternalRuleLoadResult{}, err
	}
	metrics.Detectors = len(detectors)
	metrics.Total = time.Since(totalStart)

	return ExternalRuleLoadResult{
		Detectors: detectors,
		Metrics:   metrics,
	}, nil
}

func detectorsFromBetterleaksConfig(ctx context.Context, config *blconfig.Config) ([]anonymizer.Detector, error) {
	if config == nil {
		return nil, fmt.Errorf("betterleaks config is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	detector := bldetect.NewDetectorContext(ctx, config, bldetect.ValidationOptions{})
	if detector == nil {
		return nil, fmt.Errorf("create betterleaks detector")
	}

	return []anonymizer.Detector{&betterleaksDetector{detector: detector}}, nil
}

func (d *betterleaksDetector) FindAll(text string) []anonymizer.Match {
	if text == "" {
		return nil
	}

	d.mu.Lock()
	findings := d.detector.Detect(sources.Fragment{Raw: text})
	d.mu.Unlock()

	matches := make([]anonymizer.Match, 0, len(findings))
	for _, finding := range findings {
		start, end, ok := betterleaksFindingSpan(text, finding)
		if !ok {
			continue
		}

		matches = append(matches, anonymizer.Match{
			Start:      start,
			End:        end,
			Type:       anonymizer.EntitySecret,
			Priority:   priorityMedium,
			Normalized: normalizeFold(text[start:end]),
		})
	}

	return matches
}

func betterleaksFindingSpan(text string, finding blreport.Finding) (int, int, bool) {
	secret := finding.Secret
	if secret == "" {
		return 0, 0, false
	}

	lineStarts := lineStartOffsets(text)
	matchStart, ok := offsetFromLineColumn(lineStarts, finding.StartLine, finding.StartColumn)
	if !ok && finding.StartLine > 0 {
		matchStart, ok = offsetFromLineColumn(lineStarts, finding.StartLine-1, finding.StartColumn)
	}
	if !ok {
		return fallbackSecretSpan(text, secret)
	}

	if finding.Match != "" {
		if relative := strings.Index(finding.Match, secret); relative >= 0 {
			start := matchStart + relative
			end := start + len(secret)
			if validSecretSpan(text, start, end, secret) {
				return start, end, true
			}
		}
	}

	lineEnd := lineEndOffset(text, lineStarts, finding.StartLine)
	if lineEnd < matchStart && finding.StartLine > 0 {
		lineEnd = lineEndOffset(text, lineStarts, finding.StartLine-1)
	}
	if lineEnd >= matchStart {
		if relative := strings.Index(text[matchStart:lineEnd], secret); relative >= 0 {
			start := matchStart + relative
			return start, start + len(secret), true
		}
	}

	lineStart := lineStartOffset(lineStarts, finding.StartLine)
	if lineStart < 0 && finding.StartLine > 0 {
		lineStart = lineStartOffset(lineStarts, finding.StartLine-1)
	}
	if lineStart >= 0 && lineEnd >= lineStart {
		if relative := strings.Index(text[lineStart:lineEnd], secret); relative >= 0 {
			start := lineStart + relative
			return start, start + len(secret), true
		}
	}

	return fallbackSecretSpan(text, secret)
}

func lineStartOffsets(text string) []int {
	offsets := []int{0}
	for index := range text {
		if text[index] == '\n' {
			offsets = append(offsets, index+1)
		}
	}
	return offsets
}

func offsetFromLineColumn(lineStarts []int, line, column int) (int, bool) {
	if line < 0 || line >= len(lineStarts) || column <= 0 {
		return 0, false
	}
	return lineStarts[line] + column - 1, true
}

func lineStartOffset(lineStarts []int, line int) int {
	if line < 0 || line >= len(lineStarts) {
		return -1
	}
	return lineStarts[line]
}

func lineEndOffset(text string, lineStarts []int, line int) int {
	if line < 0 || line >= len(lineStarts) {
		return -1
	}
	if line+1 < len(lineStarts) {
		return lineStarts[line+1] - 1
	}
	return len(text)
}

func fallbackSecretSpan(text, secret string) (int, int, bool) {
	start := strings.Index(text, secret)
	if start < 0 {
		return 0, 0, false
	}
	return start, start + len(secret), true
}

func validSecretSpan(text string, start, end int, secret string) bool {
	return start >= 0 && end <= len(text) && start < end && text[start:end] == secret
}
