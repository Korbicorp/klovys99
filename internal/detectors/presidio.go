package detectors

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/dlclark/regexp2"
	"gopkg.in/yaml.v3"
)

const (
	DefaultPresidioURL        = "https://raw.githubusercontent.com/microsoft/presidio/main/presidio-analyzer/presidio_analyzer/conf/default_recognizers.yaml"
	DefaultPresidioTimeout    = 10 * time.Second
	defaultPresidioSourceBase = "https://raw.githubusercontent.com/microsoft/presidio/main/presidio-analyzer/presidio_analyzer/predefined_recognizers/generic/"
)

var presidioSupportedRecognizers = map[string]string{
	"CreditCardRecognizer": "credit_card_recognizer.py",
	"CryptoRecognizer":     "crypto_recognizer.py",
	"DateRecognizer":       "date_recognizer.py",
	"EmailRecognizer":      "email_recognizer.py",
	"IbanRecognizer":       "iban_recognizer.py",
	"IpRecognizer":         "ip_recognizer.py",
	"MacAddressRecognizer": "mac_recognizer.py",
	"UrlRecognizer":        "url_recognizer.py",
}

var presidioSupportedEntityPattern = regexp.MustCompile(`supported_entity:\s*str\s*=\s*"([^"]+)"`)

type presidioConfig struct {
	Recognizers []presidioRecognizer `yaml:"recognizers"`
}

type presidioRecognizer struct {
	Name    string `yaml:"name"`
	Type    string `yaml:"type"`
	Enabled *bool  `yaml:"enabled"`
}

func LoadDefaultPresidioRules(ctx context.Context) ([]anonymizer.Detector, error) {
	return LoadPresidioRules(ctx, DefaultPresidioURL, DefaultPresidioTimeout)
}

func LoadPresidioRules(ctx context.Context, configURL string, timeout time.Duration) ([]anonymizer.Detector, error) {
	result, err := LoadPresidioRulesWithStats(ctx, configURL, timeout)
	if err != nil {
		return nil, err
	}
	return result.Detectors, nil
}

func LoadPresidioRulesWithStats(ctx context.Context, configURL string, timeout time.Duration) (ExternalRuleLoadResult, error) {
	return loadPresidioRulesWithStats(ctx, configURL, defaultPresidioSourceBase, timeout)
}

func loadPresidioRulesWithStats(ctx context.Context, configURL, sourceBaseURL string, timeout time.Duration) (ExternalRuleLoadResult, error) {
	if timeout <= 0 {
		timeout = DefaultPresidioTimeout
	}

	client := &http.Client{Timeout: timeout}
	return loadExternalPresidioRulesWithStats(ctx, client, configURL, sourceBaseURL, defaultExternalRulesCacheDir(), DefaultExternalRulesCacheTTL)
}

func LoadExternalPresidioRules(ctx context.Context, client *http.Client, configURL, sourceBaseURL string) ([]anonymizer.Detector, error) {
	result, err := loadExternalPresidioRulesWithStats(ctx, client, configURL, sourceBaseURL, "", 0)
	if err != nil {
		return nil, err
	}
	return result.Detectors, nil
}

func loadExternalPresidioRulesWithStats(ctx context.Context, client *http.Client, configURL, sourceBaseURL, cacheDir string, cacheTTL time.Duration) (ExternalRuleLoadResult, error) {
	totalStart := time.Now()
	if client == nil {
		client = &http.Client{Timeout: DefaultPresidioTimeout}
	}
	if strings.TrimSpace(configURL) == "" {
		configURL = DefaultPresidioURL
	}
	if strings.TrimSpace(sourceBaseURL) == "" {
		sourceBaseURL = defaultPresidioSourceBase
	}

	var metrics ExternalLoadMetrics
	configBody, fetchMetrics, err := loadCachedRemoteBody(ctx, client, cacheDir, "presidio", configURL, cacheTTL)
	if err != nil {
		return ExternalRuleLoadResult{}, fmt.Errorf("download presidio config: %w", err)
	}
	mergeCachedBodyMetrics(&metrics, fetchMetrics)

	var config presidioConfig
	parseStart := time.Now()
	if err := yaml.Unmarshal(configBody, &config); err != nil {
		return ExternalRuleLoadResult{}, fmt.Errorf("parse presidio config: %w", err)
	}
	metrics.Parse += time.Since(parseStart)

	var detectors []anonymizer.Detector
	loadedSources := make(map[string]struct{})
	for _, recognizer := range config.Recognizers {
		if !recognizerEnabled(recognizer) {
			continue
		}

		filename, ok := presidioSupportedRecognizers[recognizer.Name]
		if !ok {
			continue
		}
		if _, seen := loadedSources[filename]; seen {
			continue
		}
		loadedSources[filename] = struct{}{}
		metrics.Recognizers++

		sourceBody, sourceMetrics, err := loadCachedRemoteBody(
			ctx,
			client,
			cacheDir,
			"presidio",
			strings.TrimRight(sourceBaseURL, "/")+"/"+filename,
			cacheTTL,
		)
		if err != nil {
			return ExternalRuleLoadResult{}, fmt.Errorf("download presidio recognizer %q: %w", recognizer.Name, err)
		}
		mergeCachedBodyMetrics(&metrics, sourceMetrics)

		loaded, patterns, parseDuration, compileDuration, err := detectorsFromPresidioSourceWithMetrics(recognizer.Name, string(sourceBody))
		if err != nil {
			return ExternalRuleLoadResult{}, err
		}
		metrics.Parse += parseDuration
		metrics.Compile += compileDuration
		metrics.Patterns += patterns
		detectors = append(detectors, loaded...)
	}

	metrics.Detectors = len(detectors)
	metrics.Total = time.Since(totalStart)

	return ExternalRuleLoadResult{
		Detectors: detectors,
		Metrics:   metrics,
	}, nil
}

func recognizerEnabled(recognizer presidioRecognizer) bool {
	if recognizer.Enabled != nil && !*recognizer.Enabled {
		return false
	}
	return recognizer.Type == "" || recognizer.Type == "predefined"
}

func detectorsFromPresidioSource(recognizerName, source string) ([]anonymizer.Detector, error) {
	detectors, _, _, _, err := detectorsFromPresidioSourceWithMetrics(recognizerName, source)
	return detectors, err
}

func detectorsFromPresidioSourceWithMetrics(recognizerName, source string) ([]anonymizer.Detector, int, time.Duration, time.Duration, error) {
	parseStart := time.Now()
	supportedEntity, ok := extractPresidioSupportedEntity(source)
	if !ok {
		return nil, 0, 0, 0, fmt.Errorf("parse presidio recognizer %q: supported_entity not found", recognizerName)
	}

	entityType, ok := presidioEntityType(supportedEntity)
	if !ok {
		return nil, 0, time.Since(parseStart), 0, nil
	}

	patterns, err := extractPresidioPatterns(source)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("parse presidio recognizer %q: %w", recognizerName, err)
	}
	parseDuration := time.Since(parseStart)

	compileStart := time.Now()
	detectors := make([]anonymizer.Detector, 0, len(patterns))
	for _, pattern := range patterns {
		compiled, err := regexp2.Compile(pattern, regexp2.None)
		if err != nil {
			return nil, 0, parseDuration, 0, fmt.Errorf("compile presidio recognizer %q: %w", recognizerName, err)
		}
		detectors = append(detectors, regexDetector{
			entityType:       entityType,
			priority:         priorityMedium,
			pattern:          compiled,
			captureGroup:     0,
			normalizerPolicy: normalizerPolicyForEntityType(entityType),
		})
	}
	compileDuration := time.Since(compileStart)

	return detectors, len(patterns), parseDuration, compileDuration, nil
}

func extractPresidioSupportedEntity(source string) (string, bool) {
	match := presidioSupportedEntityPattern.FindStringSubmatch(source)
	if len(match) < 2 {
		return "", false
	}
	return match[1], true
}

func extractPresidioPatterns(source string) ([]string, error) {
	var patterns []string
	constants := extractPythonStringConstants(source)
	offset := 0
	for {
		index := strings.Index(source[offset:], "Pattern(")
		if index < 0 {
			break
		}
		start := offset + index + len("Pattern(")

		_, pos, ok, err := readPythonArgument(source, start)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("could not parse pattern name")
		}

		if pos >= len(source) || source[pos] != ',' {
			return nil, fmt.Errorf("pattern name is not followed by a comma")
		}
		pos++

		regexExpr, pos, ok, err := readPythonArgument(source, pos)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("could not parse pattern regex")
		}

		resolved, ok, err := resolvePythonStringExpression(regexExpr, constants)
		if err != nil {
			return nil, err
		}
		if ok {
			patterns = append(patterns, resolved)
		}

		offset = pos
	}

	return patterns, nil
}

func extractPythonStringConstants(source string) map[string]string {
	constants := make(map[string]string)
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		equalIndex := strings.Index(line, "=")
		if equalIndex <= 0 {
			continue
		}

		name := strings.TrimSpace(line[:equalIndex])
		if name == "" || strings.ContainsAny(name, " \t(") {
			continue
		}
		expr := strings.TrimSpace(line[equalIndex+1:])
		value, ok, err := resolvePythonStringExpression(expr, constants)
		if err != nil || !ok {
			continue
		}
		constants[name] = value
	}

	return constants
}

func readPythonArgument(source string, start int) (string, int, bool, error) {
	pos := skipPythonWhitespace(source, start)
	argStart := pos
	depth := 0

	for pos < len(source) {
		if end, ok, err := consumePythonStringLiteral(source, pos); err != nil {
			return "", 0, false, err
		} else if ok {
			pos = end
			continue
		}

		switch source[pos] {
		case '#':
			for pos < len(source) && source[pos] != '\n' {
				pos++
			}
			continue
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				return strings.TrimSpace(source[argStart:pos]), pos, pos > argStart, nil
			}
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(source[argStart:pos]), pos, pos > argStart, nil
			}
		}
		pos++
	}

	return strings.TrimSpace(source[argStart:pos]), pos, pos > argStart, nil
}

func skipPythonWhitespace(source string, start int) int {
	pos := start
	for pos < len(source) {
		switch source[pos] {
		case ' ', '\t', '\r', '\n':
			pos++
		default:
			return pos
		}
	}
	return pos
}

func consumePythonStringLiteral(source string, start int) (int, bool, error) {
	pos := start
	if pos >= len(source) {
		return start, false, nil
	}

	if source[pos] == 'r' || source[pos] == 'R' {
		if pos+1 >= len(source) || (source[pos+1] != '"' && source[pos+1] != '\'') {
			return start, false, nil
		}
		pos++
	}

	if source[pos] != '"' && source[pos] != '\'' {
		return start, false, nil
	}

	quote := source[pos]
	raw := pos > start
	pos++
	for pos < len(source) {
		if source[pos] == '\\' && pos+1 < len(source) && source[pos+1] == quote {
			pos += 2
			continue
		}
		if source[pos] == quote {
			return pos + 1, true, nil
		}
		if source[pos] == '\\' && !raw {
			pos += 2
			continue
		}
		pos++
	}

	return 0, false, fmt.Errorf("unterminated python string literal")
}

func resolvePythonStringExpression(expr string, constants map[string]string) (string, bool, error) {
	var builder strings.Builder
	pos := 0
	for pos < len(expr) {
		pos = skipPythonWhitespace(expr, pos)
		if pos >= len(expr) {
			break
		}
		if expr[pos] == '+' {
			pos++
			continue
		}

		end, ok, err := consumePythonStringLiteral(expr, pos)
		if err != nil {
			return "", false, err
		}
		if ok {
			value, err := decodePythonStringLiteral(expr[pos:end])
			if err != nil {
				return "", false, err
			}
			builder.WriteString(value)
			pos = end
			continue
		}

		if identifier, end, ok := consumePythonIdentifier(expr, pos); ok {
			value, exists := constants[identifier]
			if !exists {
				return "", false, nil
			}
			builder.WriteString(value)
			pos = end
			continue
		}

		return "", false, nil
	}

	return builder.String(), builder.Len() > 0, nil
}

func consumePythonIdentifier(source string, start int) (string, int, bool) {
	if start >= len(source) {
		return "", start, false
	}
	if !isPythonIdentifierStart(source[start]) {
		return "", start, false
	}

	pos := start + 1
	for pos < len(source) && isPythonIdentifierPart(source[pos]) {
		pos++
	}
	return source[start:pos], pos, true
}

func isPythonIdentifierStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isPythonIdentifierPart(value byte) bool {
	return isPythonIdentifierStart(value) || value >= '0' && value <= '9'
}

func decodePythonStringLiteral(literal string) (string, error) {
	raw := false
	if strings.HasPrefix(literal, "r") || strings.HasPrefix(literal, "R") {
		raw = true
		literal = literal[1:]
	}

	if raw {
		if len(literal) < 2 {
			return "", fmt.Errorf("invalid raw string literal")
		}
		return literal[1 : len(literal)-1], nil
	}

	return strconv.Unquote(literal)
}

func presidioEntityType(entity string) (anonymizer.EntityType, bool) {
	switch strings.TrimSpace(entity) {
	case "EMAIL_ADDRESS":
		return anonymizer.EntityEmail, true
	case "IP_ADDRESS":
		return anonymizer.EntityIP, true
	case "IBAN_CODE":
		return anonymizer.EntityIBAN, true
	case "CREDIT_CARD":
		return anonymizer.EntityCreditCard, true
	case "DATE_TIME":
		return anonymizer.EntityDate, true
	case "MAC_ADDRESS":
		return anonymizer.EntityMACAddress, true
	case "CRYPTO":
		return anonymizer.EntityCrypto, true
	default:
		return "", false
	}
}

func normalizerPolicyForEntityType(entityType anonymizer.EntityType) normalizerPolicy {
	switch entityType {
	case anonymizer.EntityIBAN, anonymizer.EntityCreditCard, anonymizer.EntityMACAddress:
		return normalizerPolicyDigitsAndLetters
	default:
		return normalizerPolicyFold
	}
}
