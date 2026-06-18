package detectors

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Korbicorp/klovis/internal/anonymizer"
	"github.com/dlclark/regexp2"
	"github.com/pelletier/go-toml/v2"
)

const (
	DefaultGitleaksURL     = "https://raw.githubusercontent.com/gitleaks/gitleaks/master/config/gitleaks.toml"
	DefaultGitleaksTimeout = 10 * time.Second
)

type gitleaksConfig struct {
	Rules []gitleaksRule `toml:"rules"`
}

type gitleaksRule struct {
	ID          string `toml:"id"`
	Regex       string `toml:"regex"`
	Path        string `toml:"path"`
	SecretGroup int    `toml:"secretGroup"`
}

func LoadDefaultGitleaksRules(ctx context.Context) ([]anonymizer.Detector, error) {
	return LoadGitleaksRules(ctx, DefaultGitleaksURL, DefaultGitleaksTimeout)
}

func LoadGitleaksRules(ctx context.Context, sourceURL string, timeout time.Duration) ([]anonymizer.Detector, error) {
	result, err := LoadGitleaksRulesWithStats(ctx, sourceURL, timeout)
	if err != nil {
		return nil, err
	}
	return result.Detectors, nil
}

func LoadGitleaksRulesWithStats(ctx context.Context, sourceURL string, timeout time.Duration) (ExternalRuleLoadResult, error) {
	if timeout <= 0 {
		timeout = DefaultGitleaksTimeout
	}
	client := &http.Client{Timeout: timeout}
	return loadExternalRulesWithStats(ctx, client, sourceURL, defaultExternalRulesCacheDir(), DefaultExternalRulesCacheTTL)
}

func LoadExternalRules(ctx context.Context, client *http.Client, sourceURL string) ([]anonymizer.Detector, error) {
	result, err := loadExternalRulesWithStats(ctx, client, sourceURL, "", 0)
	if err != nil {
		return nil, err
	}
	return result.Detectors, nil
}

func loadExternalRulesWithStats(ctx context.Context, client *http.Client, sourceURL, cacheDir string, cacheTTL time.Duration) (ExternalRuleLoadResult, error) {
	totalStart := time.Now()
	if client == nil {
		client = &http.Client{Timeout: DefaultGitleaksTimeout}
	}
	if strings.TrimSpace(sourceURL) == "" {
		sourceURL = DefaultGitleaksURL
	}

	var metrics ExternalLoadMetrics
	payload, fetchMetrics, err := loadCachedRemoteBody(ctx, client, cacheDir, "gitleaks", sourceURL, cacheTTL)
	if err != nil {
		return ExternalRuleLoadResult{}, fmt.Errorf("download gitleaks rules: %w", err)
	}
	mergeCachedBodyMetrics(&metrics, fetchMetrics)

	var config gitleaksConfig
	parseStart := time.Now()
	if err := toml.Unmarshal(payload, &config); err != nil {
		return ExternalRuleLoadResult{}, fmt.Errorf("parse gitleaks rules: %w", err)
	}
	metrics.Parse = time.Since(parseStart)
	metrics.Rules = len(config.Rules)

	compileStart := time.Now()
	detectors, err := detectorsFromGitleaksRules(config.Rules)
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

func detectorsFromGitleaksRules(rules []gitleaksRule) ([]anonymizer.Detector, error) {
	detectors := make([]anonymizer.Detector, 0, len(rules))
	for _, rule := range rules {
		if strings.TrimSpace(rule.Regex) == "" {
			continue
		}
		// Klovis scans raw stdin without file metadata, so path-scoped rules cannot be
		// applied faithfully and are skipped to avoid context-free false positives.
		if strings.TrimSpace(rule.Path) != "" {
			continue
		}

		captureGroup := rule.SecretGroup
		if captureGroup <= 0 {
			captureGroup = 0
		}

		compiled, err := regexp2.Compile(rule.Regex, regexp2.RE2)
		if err != nil {
			return nil, fmt.Errorf("compile gitleaks rule %q: %w", rule.ID, err)
		}

		detectors = append(detectors, regexDetector{
			entityType:   anonymizer.EntitySecret,
			priority:     priorityMedium,
			pattern:      compiled,
			captureGroup: captureGroup,
			normalizer:   normalizeFold,
		})
	}

	return detectors, nil
}
